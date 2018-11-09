package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/chain/txvm/errors"
	"github.com/chain/txvm/protocol/bc"
	"github.com/chain/txvm/protocol/state"
)

type blockStore struct {
	dir     string
	heights chan<- uint64

	mu     sync.Mutex
	height uint64
}

func newBlockStore(dir string, heights chan<- uint64) (*blockStore, error) {
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return nil, errors.Wrapf(err, "creating dir %s", dir)
	}

	infos, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "reading dir %s", dir)
	}

	highestDir := -1
	for _, info := range infos {
		if !info.IsDir() {
			continue
		}
		n, err := strconv.Atoi(info.Name())
		if err != nil {
			continue
		}
		if n > highestDir {
			highestDir = n
		}
	}
	var height uint64
	if highestDir >= 0 {
		subdir := path.Join(dir, fmt.Sprintf("%d", highestDir))
		infos, err := ioutil.ReadDir(subdir)
		if err != nil {
			return nil, errors.Wrapf(err, "reading %s", subdir)
		}
		for _, info := range infos {
			if info.IsDir() {
				continue
			}
			n, err := strconv.ParseUint(info.Name(), 10, 64)
			if err != nil {
				continue
			}
			if n > height {
				height = n
			}
		}
	}
	return &blockStore{
		dir:     dir,
		heights: heights,
		height:  height,
	}, nil
}

func (s *blockStore) Height(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.height, nil
}

func (s *blockStore) GetBlock(_ context.Context, height uint64) (*bc.Block, error) {
	dir, filename := blockFilename(s.dir, height)
	filename = path.Join(dir, filename)
	bits, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, errors.Wrapf(err, "reading block %d", height)
	}
	var b bc.Block
	err = b.FromBytes(bits)
	if err != nil {
		return nil, errors.Wrapf(err, "parsing block %d", height)
	}
	return &b, nil
}

func (s *blockStore) LatestSnapshot(context.Context) (*state.Snapshot, error) {
	filename := path.Join(s.dir, "snapshot")
	bits, err := ioutil.ReadFile(filename)
	if os.IsNotExist(err) {
		return state.Empty(), nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "reading snapshot")
	}

	var snapshot state.Snapshot
	err = snapshot.FromBytes(bits)
	if err != nil {
		return nil, errors.Wrap(err, "parsing snapshot")
	}
	return &snapshot, nil
}

func (s *blockStore) SaveBlock(_ context.Context, b *bc.Block) error {
	bits, err := b.Bytes()
	if err != nil {
		return errors.Wrapf(err, "storing block %d", b.Height)
	}
	dir, filename := blockFilename(s.dir, b.Height)
	err = os.MkdirAll(dir, 0700)
	if err != nil {
		return errors.Wrapf(err, "making dir %s", dir)
	}
	filename = path.Join(dir, filename)
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		got, err := ioutil.ReadFile(filename)
		if err != nil {
			return errors.Wrapf(err, "file for block %d already exists, can't tell if it's identical", b.Height)
		}
		if bytes.Equal(got, bits) {
			// File already stored.
			return nil
		}
		return fmt.Errorf("conflicting version of block %d already exists", b.Height)
	}
	if err != nil {
		return errors.Wrapf(err, "storing block %d", b.Height)
	}
	defer f.Close()
	_, err = f.Write(bits)
	return errors.Wrapf(err, "writing block %d", b.Height)
}

func (s *blockStore) FinalizeHeight(_ context.Context, height uint64) error {
	s.heights <- height
	return nil
}

func (s *blockStore) SaveSnapshot(_ context.Context, snapshot *state.Snapshot) error {
	bits, err := snapshot.Bytes()
	if err != nil {
		return errors.Wrapf(err, "saving snapshot %d", snapshot.Height())
	}
	return ioutil.WriteFile(path.Join(s.dir, "snapshot"), bits, 0600)
}

func blockFilename(root string, height uint64) (dir, filename string) {
	n := height / 1000
	dir = path.Join(root, fmt.Sprintf("%d-%d", n*1000, n*1000+999))
	filename = fmt.Sprintf("%d", height)
	return
}

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/chain/txvm/errors"
	"github.com/chain/txvm/protocol"
	"github.com/chain/txvm/protocol/bc"
	"github.com/chain/txvm/protocol/state"
	"github.com/coreos/bbolt"
)

type blockStore struct {
	db      *bbolt.DB
	heights chan<- uint64
}

func newBlockStore(db *bbolt.DB, heights chan<- uint64) (*blockStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte("root"))
		if err != nil {
			return errors.Wrap(err, "getting/creating root bucket")
		}
		heightBytes := root.Get([]byte("height"))
		if len(heightBytes) == 0 {
			blocksBucket, err := root.CreateBucket([]byte("blocks"))
			if err != nil {
				return errors.Wrap(err, "creating blocks bucket")
			}

			var height [binary.MaxVarintLen64]byte
			m := binary.PutUvarint(height[:], 1)

			initialBlock, err := protocol.NewInitialBlock(nil, 0, time.Now())
			if err != nil {
				return errors.Wrap(err, "producing genesis block")
			}

			bu, err := blocksBucket.CreateBucket(height[:m])
			if err != nil {
				return errors.Wrap(err, "creating initial-block bucket")
			}
			bbytes, err := initialBlock.Bytes()
			if err != nil {
				return errors.Wrap(err, "serializing initial block")
			}
			err = bu.Put([]byte("block"), bbytes)
			if err != nil {
				return errors.Wrap(err, "storing initial block")
			}
			err = root.Put([]byte("height"), height[:m])
			if err != nil {
				return errors.Wrap(err, "storing initial height")
			}
		}
		return nil
	})
	return &blockStore{
		db:      db,
		heights: heights,
	}, err
}

func (s *blockStore) Height(context.Context) (uint64, error) {
	var height uint64
	err := s.db.View(func(tx *bbolt.Tx) error {
		return s.getHeight(tx, &height)
	})
	return height, err
}

func (s *blockStore) getHeight(tx *bbolt.Tx, h *uint64) error {
	root := tx.Bucket([]byte("root"))  // xxx check
	bits := root.Get([]byte("height")) // xxx check
	var n int
	*h, n = binary.Uvarint(bits)
	if n < 1 {
		return errors.New("cannot parse height")
	}
	return nil
}

func (s *blockStore) GetBlock(_ context.Context, height uint64) (*bc.Block, error) {
	var b bc.Block
	err := s.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte("root"))       // xxx check
		blocks := root.Bucket([]byte("blocks")) // xxx check
		var h [binary.MaxVarintLen64]byte
		m := binary.PutUvarint(h[:], height)
		bu := blocks.Bucket(h[:m]) // xxx check
		bits := bu.Get([]byte("block"))
		return b.FromBytes(bits)
	})
	return &b, err
}

func (s *blockStore) LatestSnapshot(context.Context) (*state.Snapshot, error) {
	st := state.Empty()
	err := s.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte("root")) // xxx check
		bits := root.Get([]byte("latest_snapshot"))
		if len(bits) > 0 {
			return st.FromBytes(bits)
		}
		return nil
	})
	return st, err
}

func (s *blockStore) SaveBlock(_ context.Context, b *bc.Block) error {
	err := s.db.Update(func(tx *bbolt.Tx) error {
		var h uint64
		err := s.getHeight(tx, &h)
		if err != nil {
			return errors.Wrap(err, "getting blockstore height")
		}
		root := tx.Bucket([]byte("root"))       // xxx check
		blocks := root.Bucket([]byte("blocks")) // xxx check
		var hbits [binary.MaxVarintLen64]byte
		m := binary.PutUvarint(hbits[:], b.Height)
		bu, err := blocks.CreateBucketIfNotExists(hbits[:m])
		if err != nil {
			return errors.Wrapf(err, "creating bucket for block %d", b.Height)
		}
		bits, err := b.Bytes()
		if err != nil {
			return errors.Wrapf(err, "serializing block %d", b.Height)
		}

		exists := bu.Get([]byte("block"))
		if len(exists) > 0 {
			if !bytes.Equal(bits, exists) {
				return fmt.Errorf("conflicting block %d already exists", b.Height)
			}
			return nil
		}

		err = bu.Put([]byte("block"), bits)
		if err != nil {
			return errors.Wrapf(err, "storing block %d", b.Height)
		}
		if b.Height > h {
			root.Put([]byte("height"), hbits[:m])
		}
		return nil
	})
	return err
}

func (s *blockStore) FinalizeHeight(_ context.Context, height uint64) error {
	s.heights <- height
	return nil
}

func (s *blockStore) SaveSnapshot(_ context.Context, snapshot *state.Snapshot) error {
	sheight := snapshot.Height()
	if sheight == 0 {
		return nil
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		var h uint64
		err := s.getHeight(tx, &h)
		if err != nil {
			return errors.Wrap(err, "getting blockstore height")
		}
		root := tx.Bucket([]byte("root"))       // xxx check
		blocks := root.Bucket([]byte("blocks")) // xxx check
		var hbits [binary.MaxVarintLen64]byte
		m := binary.PutUvarint(hbits[:], sheight)
		bu, err := blocks.CreateBucketIfNotExists(hbits[:m])
		if err != nil {
			return errors.Wrapf(err, "creating bucket for snapshot %d", sheight)
		}
		bits, err := snapshot.Bytes()
		if err != nil {
			return errors.Wrapf(err, "serializing snapshot %d", sheight)
		}
		err = bu.Put([]byte("snapshot"), bits)
		if err != nil {
			return errors.Wrapf(err, "storing snapshot %d", sheight)
		}

		doStore := true

		latestBits := root.Get([]byte("latest_snapshot"))
		if len(latestBits) > 0 {
			var latest state.Snapshot
			err = latest.FromBytes(latestBits)
			if err != nil {
				return errors.Wrapf(err, "getting latest snapshot for comparison to %d", sheight)
			}
			if latest.Height() > sheight {
				doStore = false
			}
		}

		if doStore {
			return root.Put([]byte("latest_snapshot"), bits)
		}
		return nil
	})
}

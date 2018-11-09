// Command txvmbcd is a minimal TxVM blockchain server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/chain/txvm/errors"
	"github.com/chain/txvm/protocol"
	"github.com/chain/txvm/protocol/bc"
	"github.com/chain/txvm/protocol/txvm"
)

var (
	bbmu sync.Mutex
	bb   *protocol.BlockBuilder
)

var blockInterval = 5 * time.Second

var chain *protocol.Chain

func main() {
	ctx := context.Background()

	var (
		addr = flag.String("addr", "localhost:2423", "server listen address")
		dir  = flag.String("dir", "", "root of block storage tree")
	)

	flag.Parse()

	heights := make(chan uint64)
	bs, err := newBlockStore(*dir, heights)
	if err != nil {
		log.Fatal(err)
	}

	initialBlock, err := bs.GetBlock(ctx, 1)
	if os.IsNotExist(errors.Root(err)) {
		initialBlock, err = protocol.NewInitialBlock(nil, 0, time.Now())
		if err != nil {
			log.Fatal("producing genesis block: ", err)
		}
	} else if err != nil {
		log.Fatal(err)
	}

	chain, err = protocol.NewChain(ctx, initialBlock, bs, heights)
	if err != nil {
		log.Fatal("initializing Chain: ", err)
	}
	_, err = chain.Recover(ctx)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/submit", submit)
	http.HandleFunc("/get", get)
	http.ListenAndServe(*addr, nil)
}

func submit(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	defer req.Body.Close()
	prog, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading request body: %s", err), http.StatusInternalServerError)
		return
	}
	vm, err := txvm.Validate(prog, 3, math.MaxInt64)
	if err != nil {
		http.Error(w, fmt.Sprintf("validating tx: %s", err), http.StatusBadRequest)
		return
	}
	runlimit := math.MaxInt64 - vm.Runlimit()
	tx, err := bc.NewTx(prog, 3, runlimit)
	if err != nil {
		http.Error(w, fmt.Sprintf("building tx: %s", err), http.StatusBadRequest)
		return
	}

	bbmu.Lock()
	defer bbmu.Unlock()

	if bb == nil {
		bb = protocol.NewBlockBuilder()
		nextBlockTime := time.Now().Add(blockInterval)
		log.Printf("starting new tx pool to commit at %s", nextBlockTime)
		err := bb.Start(chain.State(), bc.Millis(nextBlockTime))
		if err != nil {
			http.Error(w, fmt.Sprintf("starting a new tx pool: %s", err), http.StatusInternalServerError)
			return
		}
		time.AfterFunc(blockInterval, func() {
			bbmu.Lock()
			defer bbmu.Unlock()

			unsignedBlock, newSnapshot, err := bb.Build()
			if err != nil {
				log.Fatal(errors.Wrap(err, "building new block"))
			}
			err = chain.CommitAppliedBlock(ctx, &bc.Block{UnsignedBlock: unsignedBlock}, newSnapshot)
			if err != nil {
				log.Fatal(errors.Wrap(err, "committing new block"))
			}
		})
	}

	err = bb.AddTx(bc.NewCommitmentsTx(tx))
	if err != nil {
		http.Error(w, fmt.Sprintf("adding tx to pool: %s", err), http.StatusBadRequest)
		return
	}
	log.Printf("adding tx %x to pool", tx.ID.Bytes())
	w.WriteHeader(http.StatusNoContent)
}

func get(w http.ResponseWriter, req *http.Request) {
	wantStr := req.FormValue("height")
	var (
		want uint64
		err  error
	)
	if wantStr != "" {
		want, err = strconv.ParseUint(wantStr, 10, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("parsing height: %s", err), http.StatusBadRequest)
			return
		}
	}

	height := chain.Height()
	if want == 0 {
		want = height
	}
	if want > height {
		ctx := req.Context()
		waiter := chain.BlockWaiter(want)
		select {
		case <-waiter:
			// ok
		case <-ctx.Done():
			http.Error(w, "timed out", http.StatusRequestTimeout)
			return
		}
	}

	ctx := req.Context()

	b, err := chain.GetBlock(ctx, want)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting block %d: %s", want, err), http.StatusInternalServerError)
		return
	}

	bits, err := b.Bytes()
	if err != nil {
		http.Error(w, fmt.Sprintf("serializing block %d: %s", want, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, err = w.Write(bits)
	if err != nil {
		http.Error(w, fmt.Sprintf("sending response: %s", err), http.StatusInternalServerError)
	}
}

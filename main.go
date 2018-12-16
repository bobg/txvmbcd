// Command txvmbcd is a minimal TxVM blockchain server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/chain/txvm/errors"
	"github.com/chain/txvm/protocol"
	"github.com/chain/txvm/protocol/bc"
	"github.com/coreos/bbolt"
	"github.com/golang/protobuf/proto"
)

var (
	bbmu sync.Mutex
	bb   *protocol.BlockBuilder
)

var blockInterval = 5 * time.Second

var (
	initialBlock *bc.Block
	chain        *protocol.Chain
)

func main() {
	ctx := context.Background()

	var (
		addr   = flag.String("addr", "localhost:2423", "server listen address")
		dbfile = flag.String("db", "", "path to block storage db")
	)

	flag.Parse()

	db, err := bbolt.Open(*dbfile, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}

	heights := make(chan uint64)
	bs, err := newBlockStore(db, heights)
	if err != nil {
		log.Fatal(err)
	}

	initialBlock, err = bs.GetBlock(ctx, 1)
	if err != nil {
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

	initialBlockID := initialBlock.Hash()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("listening on %s, initial block ID %x", listener.Addr(), initialBlockID.Bytes())

	http.HandleFunc("/submit", submit)
	http.HandleFunc("/get", get)
	http.Serve(listener, nil)
}

func submit(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	bits, err := ioutil.ReadAll(req.Body)
	if err != nil {
		httpErrf(w, http.StatusInternalServerError, "reading request body: %s", err)
		return
	}

	var rawTx bc.RawTx
	err = proto.Unmarshal(bits, &rawTx)
	if err != nil {
		httpErrf(w, http.StatusBadRequest, "parsing request body: %s", err)
		return
	}

	tx, err := bc.NewTx(rawTx.Program, rawTx.Version, rawTx.Runlimit)
	if err != nil {
		httpErrf(w, http.StatusBadRequest, "building tx: %s", err)
		return
	}

	bbmu.Lock()
	defer bbmu.Unlock()

	if bb == nil {
		bb = protocol.NewBlockBuilder()
		nextBlockTime := time.Now().Add(blockInterval)

		st := chain.State()
		if st.Header == nil {
			err = st.ApplyBlockHeader(initialBlock.BlockHeader)
			if err != nil {
				httpErrf(w, http.StatusInternalServerError, "initializing empty state: %s", err)
				return
			}
		}

		err := bb.Start(chain.State(), bc.Millis(nextBlockTime))
		if err != nil {
			httpErrf(w, http.StatusInternalServerError, "starting a new tx pool: %s", err)
			return
		}
		log.Printf("starting new block, will commit at %s", nextBlockTime)
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
			log.Printf("committed block %d with %d transaction(s)", unsignedBlock.Height, len(unsignedBlock.Transactions))

			bb = nil
		})
	}

	err = bb.AddTx(bc.NewCommitmentsTx(tx))
	if err != nil {
		httpErrf(w, http.StatusBadRequest, "adding tx to pool: %s", err)
		return
	}
	log.Printf("added tx %x to the pending block", tx.ID.Bytes())
	w.WriteHeader(http.StatusNoContent)
}

func get(w http.ResponseWriter, req *http.Request) {
	wantStr := req.FormValue("height")
	var (
		want uint64 = 1
		err  error
	)
	if wantStr != "" {
		want, err = strconv.ParseUint(wantStr, 10, 64)
		if err != nil {
			httpErrf(w, http.StatusBadRequest, "parsing height: %s", err)
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
			httpErrf(w, http.StatusRequestTimeout, "timed out")
			return
		}
	}

	ctx := req.Context()

	b, err := chain.GetBlock(ctx, want)
	if err != nil {
		httpErrf(w, http.StatusInternalServerError, "getting block %d: %s", want, err)
		return
	}

	bits, err := b.Bytes()
	if err != nil {
		httpErrf(w, http.StatusInternalServerError, "serializing block %d: %s", want, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, err = w.Write(bits)
	if err != nil {
		httpErrf(w, http.StatusInternalServerError, "sending response: %s", err)
		return
	}
}

func httpErrf(w http.ResponseWriter, code int, msgfmt string, args ...interface{}) {
	http.Error(w, fmt.Sprintf(msgfmt, args...), code)
	log.Printf(msgfmt, args...)
}

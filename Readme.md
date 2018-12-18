# Txvmbcd

This is Txvmbcd,
a minimal blockchain server based on
[TxVM](https://github.com/chain/txvm).

## Usage

```sh
$ txvmbcd [-addr LISTENADDR] -db DBFILE
```

This will read DBFILE if it exists and create it if it doesn’t.
DBFILE is a
[Sqlite3](https://www.sqlite.org/)
file for storing the blocks in the blockchain.
If this is a new blockchain,
a genesis block
(at height 1)
is automatically created and added to the database.

On startup,
`txvmbcd` reports the genesis block hash and its listen address
(default `localhost:2423` unless overridden with `-addr`).

Callers may submit proposed transactions for the blockchain with a `POST` request to the `/submit` URL.
The body of the request must be a serialized
[bc.RawTx](https://godoc.org/github.com/chain/txvm/protocol/bc#RawTx).
The `/submit` request returns immediately.
The server pools the transaction proposal with others that arrive in a five-second span,
then produces a new block for the chain.

Callers may request blocks from the server’s database with a `GET` request to `/get`.
The URL may include `?height=N` where N is the height of the desired block.
If omitted,
N defaults to 1.
It can be set to 0 to request the highest block.
If N is greater than the height of the highest block,
the request will block until the desired block is available.
It is thus possible to “long poll” for blocks.
The response is a serialized [bc.Block](https://godoc.org/github.com/chain/txvm/protocol/bc#Block).

## Example

This example demonstrates how to populate a new txvmbcd blockchain using the command-line tools from the txvm project.

Install the binaries from github.com/chain/txvm:

```sh
$ go install github.com/chain/txvm/cmd/...
```

Install `txvmbcd`:

```sh
$ txvmbcd -db txvmbcd.db
```

This will create the file `txvmbcd.db` in the current directory and produce log output in the shell,
including the listen address of the txvmbcd server,
and the hash of the initial block,
both of which you’ll need later.

In a separate shell,
create a private/public keypair for an asset issuer:

```sh
$ ed25519 gen | tee issuer.prv | ed25519 pub >issuer.pub
```

Compute the ID of the default asset produced by this issuer:

```sh
$ assetid 1 `hex <issuer.pub` >asset-id
```

Create a private/public keypair for the recipient of some issued funds:

```sh
$ ed25519 gen | tee recipient.prv | ed25519 pub >recipient.pub
```

Build and submit a transaction to the blockchain that issues 100 units of the issuer’s asset,
sending them to the recipient.

```sh
$ tx build issue -blockchain BLOCKCHAINID -quorum 1 -prv `hex <issuer.prv` -pub `hex <issuer.pub` -amount 100 output -quorum 1 -pub `hex <recipient.pub` -amount 100 -assetid `hex <asset-id` | curl --data-binary @- http://LISTENADDR/submit
```

Here,
BLOCKCHAINID is the hash of the initial block and LISTENADDR is the txvmbcd listen address,
both reported when txvmbcd started.

Watch the server log.
In five seconds you should see a message like:

```
committed block 2 with 1 transaction(s)
```

The blockchain is now initialized and populated with 100 units of the issuer’s asset,
controlled by the recipient.
You can see the header of block two by requesting it from the server like this:

```sh
$ curl -s 'http://LISTENADDR/get?height=2' | block header -pretty
```

You can see the first (and only) transaction in this block like this:

```sh
$ curl -s 'http://LISTENADDR/get?height=2' | block tx -pretty 0
```

And you can disassemble the bytes of the transaction’s program like this:

```sh
$ curl -s 'http://LISTENADDR/get?height=2' | block tx 0 | asm -d
```

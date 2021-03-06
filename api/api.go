package api

import (
	"fmt"
	"github.com/aead/chacha20/chacha"
	"hash"
	"runtime"
	"sync"
)

// Copyright © 2022 Matthew R Bonnette. Licensed under the Apache-2.0 license.
// This file contains a Go-specific API implementing the standard hash.Hash interface.

/* TODO: Export digest for parallelized hashing and make Sum() align with its expected serial, single-threaded usage in hash.Hash. */
type digest struct {
	ln, dex uint
	ch      chan block
	tree    map[uint][]byte
	summing sync.WaitGroup
	mapping sync.Mutex
	lag     []byte
}

type block struct {
	dex   uint
	bytes []byte
}

var threads = runtime.NumCPU()

func KeySize() int { return 24 }

func (d *digest) Size() int { return int(d.ln) }

func (d *digest) BlockSize() int { return bytesPerBlock }

func New(ln uint) hash.Hash {
	d := &digest{ln: ln, tree: map[uint][]byte{}}
	d.initWorkers()
	return d
}

/* TODO: Investigate that 1GiB inputs switch between allocating 4.5 and 6.1 MB, a process that dramatically affects performance. */
func (d *digest) Write(buf []byte) (int, error) {
	count := len(buf)
	if len(d.lag) > 0 {
		buf = append(d.lag, buf...)
		d.lag = d.lag[:0]
	}

	for len(buf) >= bytesPerBlock {
		d.ch <- block{d.dex, buf[:bytesPerBlock]}
		buf = buf[bytesPerBlock:]
		d.dex++
	}
	if len(buf) > 0 {
		d.lag = append(d.lag, buf...)
	}

	return count, nil
}

func (d *digest) Sum(key []byte) []byte {
	/* TODO: See if RWMutex + an atomic counter is a better method of syncronization,
	as we could reuse the goroutines instead of killing them each time Sum() is called. */
	close(d.ch) /* Parallel hashing operations are terminated. */
	if len(key) == 0 {
		/* If key is nil or []byte(nil) or []byte{}, unkeyed hashing is assumed. */
		key = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	} else if len(key) != 24 {
		panic(fmt.Errorf("rathash: Sum: key size of %d invalid, must be 24", len(key)))
	}

	if len(d.lag) > 0 {
		d.consume(block{d.dex, d.lag})
		d.dex++
	} else if d.dex == 0 {
		/* This can happen if 0 bytes were written to d. */
		d.consume(block{0, nil})
		d.dex++
	}
	d.summing.Wait()

	/* TODO: Implement product hashing. */
	final := make([]byte, d.ln)
	chacha.XORKeyStream(final, final, key, d.tree[d.dex-1], 8)

	/* State alterations, if any, are undone. */
	if len(d.lag) > 0 || d.dex == 0 {
		d.dex--
		delete(d.tree, d.dex)
	}
	d.initWorkers() /* Parallel hashing threads are re-established. */

	return final
}

func (d *digest) Reset() {
	/* TODO: Ensure that secret information is being securely erased. */
	d.dex, d.lag = 0, nil
	for k := range d.tree {
		delete(d.tree, k)
	}
}

func (d *digest) initWorkers() {
	d.ch = make(chan block, threads*2)
	d.summing.Add(threads)
	for i := threads; i > 0; i-- {
		go func() {
			for {
				b, ok := <-d.ch
				if !ok {
					d.summing.Done()
					return
				}
				d.consume(b)
			}
		}()
	}
}

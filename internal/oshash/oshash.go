// Package oshash computes the OpenSubtitles hash of a file: the file size
// plus the little-endian uint64 sums of its first and last 64 KiB.
package oshash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const chunkSize = 64 << 10

// MinSize is the smallest file size that has a hash.
const MinSize = 2 * chunkSize

// ErrTooSmall is returned for files smaller than MinSize.
var ErrTooSmall = errors.New("oshash: file is smaller than 128 KiB")

// Hash is an OpenSubtitles hash.
type Hash uint64

// String returns the hash as 16 lowercase hex digits, the format that
// OpenSubtitles expects.
func (h Hash) String() string {
	return fmt.Sprintf("%016x", uint64(h))
}

// Compute returns the hash of r, which holds size bytes.
func Compute(r io.ReaderAt, size int64) (Hash, error) {
	if size < MinSize {
		return 0, ErrTooSmall
	}

	h := uint64(size)
	buf := make([]byte, chunkSize)
	for _, off := range []int64{0, size - chunkSize} {
		n, err := r.ReadAt(buf, off)
		if n < chunkSize {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		for i := 0; i < chunkSize; i += 8 {
			h += binary.LittleEndian.Uint64(buf[i:])
		}
	}

	return Hash(h), nil
}

// File returns the hash and the size of the file at path.
func File(path string) (Hash, int64, error) {
	f, err := os.Open(path) //nolint:gosec // hashing the files that the user selected is the purpose
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}

	h, err := Compute(f, fi.Size())
	return h, fi.Size(), err
}

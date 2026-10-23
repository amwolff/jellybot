package oshash

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// reference computes the hash with arbitrary-precision arithmetic over the
// whole buffer, independently of the chunked implementation.
func reference(data []byte) Hash {
	sum := new(big.Int).SetInt64(int64(len(data)))
	add := func(chunk []byte) {
		for i := 0; i < len(chunk); i += 8 {
			var w big.Int
			// Little-endian: reverse the 8 bytes into big-endian order.
			be := make([]byte, 8)
			for j := range 8 {
				be[j] = chunk[i+7-j]
			}
			w.SetBytes(be)
			sum.Add(sum, &w)
		}
	}
	add(data[:chunkSize])
	add(data[len(data)-chunkSize:])
	mod := new(big.Int).Lsh(big.NewInt(1), 64)
	sum.Mod(sum, mod)
	return Hash(sum.Uint64())
}

func TestCompute(t *testing.T) {
	// splitmix64 gives deterministic test data.
	state := uint64(0x9E3779B97F4A7C15)
	random := func(n int) []byte {
		b := make([]byte, n+8)
		for i := 0; i < n; i += 8 {
			state += 0x9E3779B97F4A7C15
			z := state
			z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
			z = (z ^ (z >> 27)) * 0x94D049BB133111EB
			binary.LittleEndian.PutUint64(b[i:], z^(z>>31))
		}
		return b[:n]
	}

	tests := []struct {
		name string
		data []byte
		want Hash
	}{
		{name: "zeros", data: make([]byte, 1<<20), want: Hash(1 << 20)},
		// Each 0xFF word is 2^64-1, which subtracts one per word: 2*8192 words.
		{name: "ones", data: bytes.Repeat([]byte{0xFF}, 1<<20), want: Hash(1<<20 - 2*chunkSize/8)},
		{name: "exactly 128 KiB", data: random(MinSize), want: 0},
		{name: "overlap-free odd size", data: random(MinSize + 13), want: 0},
		{name: "large random", data: random(3<<20 + 7), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == 0 {
				want = reference(tt.data)
			}
			got, err := Compute(bytes.NewReader(tt.data), int64(len(tt.data)))
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			if got != want {
				t.Fatalf("Compute = %s, want %s", got, want)
			}
			if ref := reference(tt.data); got != ref {
				t.Fatalf("Compute = %s, reference = %s", got, ref)
			}
		})
	}
}

func TestComputeTooSmall(t *testing.T) {
	for _, n := range []int{0, 1, MinSize - 1} {
		_, err := Compute(bytes.NewReader(make([]byte, n)), int64(n))
		if !errors.Is(err, ErrTooSmall) {
			t.Fatalf("size %d: err = %v, want ErrTooSmall", n, err)
		}
	}
}

func TestComputeShortReader(t *testing.T) {
	// The reader holds fewer bytes than the declared size.
	_, err := Compute(bytes.NewReader(make([]byte, MinSize)), MinSize+100)
	if err == nil {
		t.Fatal("expected an error for a short reader")
	}
}

func TestString(t *testing.T) {
	if got := Hash(0x8e245d9679d31e12).String(); got != "8e245d9679d31e12" {
		t.Fatalf("String = %q", got)
	}
	if got := Hash(0x1).String(); got != "0000000000000001" {
		t.Fatalf("String = %q", got)
	}
}

func TestFile(t *testing.T) {
	data := bytes.Repeat([]byte{0x01, 0x02, 0x03}, 100_000)
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h, size, err := File(path)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", size, len(data))
	}
	if want := reference(data); h != want {
		t.Fatalf("hash = %s, want %s", h, want)
	}
}

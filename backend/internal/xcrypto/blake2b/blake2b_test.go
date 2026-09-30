// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package blake2b

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// RFC 7693 Appendix A: BLAKE2b-512("abc").
var rfc7693abc512 = []byte{
	0xba, 0x80, 0xa5, 0x3f, 0x98, 0x1c, 0x4d, 0x0d, 0x6a, 0x27, 0x97, 0xb6, 0x9f, 0x12, 0xf6, 0xe9,
	0x4c, 0x21, 0x2f, 0x14, 0x68, 0x5a, 0xc4, 0xb7, 0x4b, 0x12, 0xbb, 0x6f, 0xdb, 0xff, 0xa2, 0xd1,
	0x7d, 0x87, 0xc5, 0x39, 0x2a, 0xab, 0x79, 0x2d, 0xc2, 0x52, 0xd5, 0xde, 0x45, 0x33, 0xcc, 0x95,
	0x18, 0xd3, 0x8a, 0xa8, 0xdb, 0xf1, 0x92, 0x5a, 0xb9, 0x23, 0x86, 0xed, 0xd4, 0x00, 0x99, 0x23,
}

// BLAKE2b-256("abc") and BLAKE2b-256("") — the digest size Nimiq addresses
// and transaction hashes are built on (values from the reference
// implementation's generated KATs).
var (
	abc256 = []byte{
		0xbd, 0xdd, 0x81, 0x3c, 0x63, 0x42, 0x39, 0x72, 0x31, 0x71, 0xef, 0x3f, 0xee, 0x98, 0x57, 0x9b,
		0x94, 0x96, 0x4e, 0x3b, 0xb1, 0xcb, 0x3e, 0x42, 0x72, 0x62, 0xc8, 0xc0, 0x68, 0xd5, 0x23, 0x19,
	}
	empty256 = []byte{
		0x0e, 0x57, 0x51, 0xc0, 0x26, 0xe5, 0x43, 0xb2, 0xe8, 0xab, 0x2e, 0xb0, 0x60, 0x99, 0xda, 0xa1,
		0xd1, 0xe5, 0xdf, 0x47, 0x77, 0x8f, 0x77, 0x87, 0xfa, 0xab, 0x45, 0xcd, 0xf1, 0x2f, 0xe3, 0xa8,
	}
)

func TestSum512RFC7693(t *testing.T) {
	got := Sum512([]byte("abc"))
	if !bytes.Equal(got[:], rfc7693abc512) {
		t.Fatalf("Sum512(abc) = %s, want %s", hex.EncodeToString(got[:]), hex.EncodeToString(rfc7693abc512))
	}
}

func TestSum256(t *testing.T) {
	if got := Sum256([]byte("abc")); !bytes.Equal(got[:], abc256) {
		t.Fatalf("Sum256(abc) = %s", hex.EncodeToString(got[:]))
	}
	if got := Sum256(nil); !bytes.Equal(got[:], empty256) {
		t.Fatalf("Sum256(\"\") = %s", hex.EncodeToString(got[:]))
	}
}

// New256 must agree with Sum256 however the input is chunked, including
// across the 128-byte block boundary.
func TestStreamingMatchesOneShot(t *testing.T) {
	msg := bytes.Repeat([]byte("nimiq "), 100) // 600 bytes, several blocks
	want := Sum256(msg)
	for _, chunk := range []int{1, 7, 64, 127, 128, 129, 600} {
		h, err := New256(nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(msg); i += chunk {
			end := i + chunk
			if end > len(msg) {
				end = len(msg)
			}
			h.Write(msg[i:end])
		}
		if got := h.Sum(nil); !bytes.Equal(got, want[:]) {
			t.Fatalf("chunk %d: %s != %s", chunk, hex.EncodeToString(got), hex.EncodeToString(want[:]))
		}
	}
}

func TestKeyedAndSizes(t *testing.T) {
	if _, err := New(0, nil); err == nil {
		t.Fatal("size 0 accepted")
	}
	if _, err := New(Size+1, nil); err == nil {
		t.Fatal("size 65 accepted")
	}
	if _, err := New512(bytes.Repeat([]byte{1}, Size+1)); err == nil {
		t.Fatal("65-byte key accepted")
	}
	keyed, err := New256([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	keyed.Write([]byte("abc"))
	if got := keyed.Sum(nil); bytes.Equal(got, abc256) {
		t.Fatal("keyed digest equals unkeyed digest")
	}
	if keyed.Size() != Size256 || keyed.BlockSize() != BlockSize {
		t.Fatalf("Size/BlockSize = %d/%d", keyed.Size(), keyed.BlockSize())
	}
}

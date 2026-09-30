// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package argon2

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// RFC 9106 §5 test vectors (password 32×0x01, salt 16×0x02, secret 8×0x03,
// associated data 12×0x04, t=3, m=32 KiB, p=4, 32-byte tag).
var (
	genKatPassword = bytes.Repeat([]byte{0x01}, 32)
	genKatSalt     = bytes.Repeat([]byte{0x02}, 16)
	genKatSecret   = bytes.Repeat([]byte{0x03}, 8)
	genKatAAD      = bytes.Repeat([]byte{0x04}, 12)
)

func TestArgon2d(t *testing.T) {
	want := []byte{
		0x51, 0x2b, 0x39, 0x1b, 0x6f, 0x11, 0x62, 0x97,
		0x53, 0x71, 0xd3, 0x09, 0x19, 0x73, 0x42, 0x94,
		0xf8, 0x68, 0xe3, 0xbe, 0x39, 0x84, 0xf3, 0xc1,
		0xa1, 0x3a, 0x4d, 0xb9, 0xfa, 0xbe, 0x4a, 0xcb,
	}
	hash := deriveKey(argon2d, genKatPassword, genKatSalt, genKatSecret, genKatAAD, 3, 32, 4, 32)
	if !bytes.Equal(hash, want) {
		t.Errorf("argon2d: got %s, want %s", hex.EncodeToString(hash), hex.EncodeToString(want))
	}
}

func TestArgon2i(t *testing.T) {
	want := []byte{
		0xc8, 0x14, 0xd9, 0xd1, 0xdc, 0x7f, 0x37, 0xaa,
		0x13, 0xf0, 0xd7, 0x7f, 0x24, 0x94, 0xbd, 0xa1,
		0xc8, 0xde, 0x6b, 0x01, 0x6d, 0xd3, 0x88, 0xd2,
		0x99, 0x52, 0xa4, 0xc4, 0x67, 0x2b, 0x6c, 0xe8,
	}
	hash := deriveKey(argon2i, genKatPassword, genKatSalt, genKatSecret, genKatAAD, 3, 32, 4, 32)
	if !bytes.Equal(hash, want) {
		t.Errorf("argon2i: got %s, want %s", hex.EncodeToString(hash), hex.EncodeToString(want))
	}
}

func TestArgon2id(t *testing.T) {
	want := []byte{
		0x0d, 0x64, 0x0d, 0xf5, 0x8d, 0x78, 0x76, 0x6c,
		0x08, 0xc0, 0x37, 0xa3, 0x4a, 0x8b, 0x53, 0xc9,
		0xd0, 0x1e, 0xf0, 0x45, 0x2d, 0x75, 0xb6, 0x5e,
		0xb5, 0x25, 0x20, 0xe9, 0x6b, 0x01, 0xe6, 0x59,
	}
	hash := deriveKey(argon2id, genKatPassword, genKatSalt, genKatSecret, genKatAAD, 3, 32, 4, 32)
	if !bytes.Equal(hash, want) {
		t.Errorf("argon2id: got %s, want %s", hex.EncodeToString(hash), hex.EncodeToString(want))
	}
}

// IDKey is the public entry point the admin password hashes go through: it
// must be deterministic, sensitive to every input, and honour keyLen.
func TestIDKeyProperties(t *testing.T) {
	pw, salt := []byte("correct horse battery staple"), []byte("0123456789abcdef")
	a := IDKey(pw, salt, 1, 8*1024, 2, 32)
	b := IDKey(pw, salt, 1, 8*1024, 2, 32)
	if !bytes.Equal(a, b) {
		t.Fatal("IDKey is not deterministic")
	}
	if len(a) != 32 {
		t.Fatalf("keyLen: got %d bytes", len(a))
	}
	if bytes.Equal(a, IDKey([]byte("correct horse battery staplf"), salt, 1, 8*1024, 2, 32)) {
		t.Fatal("password change did not change the hash")
	}
	if bytes.Equal(a, IDKey(pw, []byte("0123456789abcdeg"), 1, 8*1024, 2, 32)) {
		t.Fatal("salt change did not change the hash")
	}
	if bytes.Equal(a, IDKey(pw, salt, 2, 8*1024, 2, 32)) {
		t.Fatal("time parameter change did not change the hash")
	}
}

func BenchmarkIDKey(b *testing.B) {
	pw, salt := []byte("password"), []byte("0123456789abcdef")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		IDKey(pw, salt, 1, 64*1024, 4, 32)
	}
}

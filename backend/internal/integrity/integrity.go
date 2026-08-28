// Package integrity lets a running nim.shop backend PROVE what it is.
//
// At build time the source manifest (internal/integrity/source.integrity.json,
// produced by tools/integrity/generate.mjs) is embedded into the binary with
// //go:embed. At runtime the binary hashes its own executable and reports both
// values at GET /api/integrity:
//
//	{
//	  "binary_sha256":  "...",           // hash of the running executable
//	  "go_version":     "go1.22.5",
//	  "source_manifest": { rootHash, files:[...] }   // provenance baked in
//	}
//
// Verifier chain (see tools/integrity/README.md):
//
//	published source root  ──authenticates──▶  embedded source_manifest.rootHash
//	published binary hash  ──authenticates──▶  binary_sha256
//	anyone rebuilding the source with tools/integrity/build-reproducible.sh
//	gets the same binary hash  ──links──▶  binary is built from that source
package integrity

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// sourceManifestJSON is the backend source manifest generated at build time and
// baked into the binary. It is excluded from its own hashing (circular).
//
//go:embed source.integrity.json
var sourceManifestJSON []byte

var (
	binaryOnce sync.Once
	binaryHex  string
	binaryErr  error
)

// BinarySHA256 hashes the running executable once (resolved through symlinks,
// e.g. Linux /proc/self/exe) and caches the result for the process lifetime.
func BinarySHA256() (string, error) {
	binaryOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			binaryErr = err
			return
		}
		if real, e := filepath.EvalSymlinks(exe); e == nil {
			exe = real
		}
		f, err := os.Open(exe)
		if err != nil {
			binaryErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			binaryErr = err
			return
		}
		binaryHex = hex.EncodeToString(h.Sum(nil))
	})
	return binaryHex, binaryErr
}

// SourceRootHash returns the root hash recorded in the embedded manifest, or
// "" if the manifest could not be parsed.
func SourceRootHash() string {
	var m struct {
		RootHash string `json:"rootHash"`
	}
	if err := json.Unmarshal(sourceManifestJSON, &m); err != nil {
		return ""
	}
	return m.RootHash
}

// Report assembles the public integrity response served at /api/integrity.
func Report() map[string]interface{} {
	out := map[string]interface{}{
		"schema":          "nimiq-shop.integrity/v1",
		"go_version":      runtime.Version(),
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"source_root":     SourceRootHash(),
		"source_manifest": json.RawMessage(sourceManifestJSON),
	}
	if bin, err := BinarySHA256(); err == nil {
		out["binary_sha256"] = bin
	} else {
		out["binary_sha256"] = nil
		out["binary_error"] = err.Error()
	}
	return out
}

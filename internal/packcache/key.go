package packcache

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeriveKey computes a stable cache key for one upload-pack response.
//
// The key must capture everything that determines the pack bytes:
//   - repoID: which repository (workspace/uuid).
//   - generation: a token that changes whenever the repo's objects/refs change
//     (bumped on push/gc). Including it means a push invalidates prior entries
//     without an explicit delete — a stale generation simply never matches.
//   - requestBody: the raw `git upload-pack --stateless-rpc` negotiation
//     (wants / haves / capabilities). Identical CI clones send byte-identical
//     bodies, so they collapse to one key; a different fetch shape gets its own.
//
// A collision here would serve the wrong pack, so the inputs are length-prefixed
// before hashing to avoid ambiguity between concatenations.
func DeriveKey(repoID, generation string, requestBody []byte) string {
	h := sha256.New()
	writeField(h, []byte(repoID))
	writeField(h, []byte(generation))
	writeField(h, requestBody)
	return hex.EncodeToString(h.Sum(nil))
}

// writeField appends an 8-byte big-endian length prefix followed by the bytes,
// so distinct field boundaries can never alias (e.g. "ab"+"c" vs "a"+"bc").
func writeField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var l [8]byte
	n := uint64(len(b))
	for i := 7; i >= 0; i-- {
		l[i] = byte(n)
		n >>= 8
	}
	_, _ = h.Write(l[:])
	_, _ = h.Write(b)
}

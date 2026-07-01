// Package packcache is the foundation for the pack-objects cache (C3), a
// Gitaly-style disk cache that deduplicates identical `git upload-pack`
// responses so a burst of identical clones (e.g. 7 parallel CI jobs cloning the
// same repo+SHA) generates the pack ONCE and streams the cached bytes to every
// caller, instead of running N concurrent pack-objects processes.
//
// This is distinct from bitmap pack REUSE (git-native, per-clone; enabled by
// the maintenance atomic-bitmap fix). Pack reuse makes each clone cheap; this
// cache removes redundant generations ACROSS concurrent identical clones. The
// two compose. See Issue-5071 and Doc-635 for the full rationale and the
// GitLab/Gitaly reference.
//
// # Status (foundation CR)
//
// Implemented and tested here:
//   - Config (flag-gated, default off) + env loading.
//   - Cache key derivation (repo id + upload-pack request body + generation).
//   - Disk store: atomic Put (temp + rename), TTL-bounded Get, single-flight
//     group so concurrent identical keys coalesce onto one generation.
//
// Deliberately NOT wired yet (follow-up commits in this CR):
//   - Wiring into internal/protocol.HandleUploadPack (generate-through-cache,
//     stream replay to the HTTP response). Kept separate so it lands after the
//     CR-7080 upload-pack changes merge, avoiding a conflict.
//   - Invalidation on push / gc (bump the repo generation token).
//   - Cache size accounting / eviction against MaxBytes.
//   - Metrics (hit/miss/coalesced/bytes-saved).
package packcache

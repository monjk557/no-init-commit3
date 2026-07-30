package ruleguard

import "context"

// shadow.go implements Phase 1 of the migration (design §10): the executor
// evaluates the bundle LOCALLY but the REMOTE verdict (ValidatePushRef) stays
// authoritative. The only job here is to compare the two and make any divergence
// loud — Phase 1 never lets local evaluation change a push's outcome. This is the
// deliberate counter to the previous silent fail-open: a fallback is still
// possible, but taking it is always logged (§8).
//
// The bundle transport (SSH handshake token / HTTP X-Rule-Bundle header →
// push context, tasks 3/4/5) is not built yet, so the receipt point is an
// interface over env vars (see LoadShadowBundle). When those land, only the
// receipt wiring changes; the comparison below is unaffected.

// BundleStatus classifies whether a received bundle can drive local evaluation,
// and if not, why (drives the §8 skip-reason log).
type BundleStatus string

const (
	BundleOK                BundleStatus = "ok"
	BundleAbsent            BundleStatus = "absent"             // no bundle delivered (pre-wiring, or gateway omitted)
	BundleOverflow          BundleStatus = "overflow"           // gateway dropped it for exceeding the 32KB ceiling (§5.1)
	BundleUnsupportedSchema BundleStatus = "unsupported-schema" // schema_version this executor can't evaluate (§7.3)
	BundleDecodeError       BundleStatus = "decode-error"       // corrupt gzip/base64/JSON
)

// BundleSource is the outcome of trying to obtain a usable bundle. Bundle is
// non-nil only when Status == BundleOK. Err carries the underlying decode failure
// for logging when Status == BundleDecodeError.
type BundleSource struct {
	Bundle *Bundle
	Status BundleStatus
	Err    error
}

// LoadShadowBundle interprets the transport inputs (design §5.1) into a usable
// bundle or the reason one isn't available:
//
//   - overflow flag set  → BundleOverflow  (gateway omitted an over-ceiling bundle)
//   - empty payload      → BundleAbsent    (nothing delivered)
//   - decode failure     → BundleDecodeError
//   - unknown schema     → BundleUnsupportedSchema
//   - otherwise          → BundleOK
//
// Every non-OK status means "skip local eval, fall back to remote, and LOG it"
// (§8) — never a silent skip.
func LoadShadowBundle(encoded string, overflow bool) BundleSource {
	if overflow {
		return BundleSource{Status: BundleOverflow}
	}
	if encoded == "" {
		return BundleSource{Status: BundleAbsent}
	}
	b, err := DecodeBundle(encoded)
	if err != nil {
		return BundleSource{Status: BundleDecodeError, Err: err}
	}
	if !b.SchemaSupported() {
		return BundleSource{Bundle: b, Status: BundleUnsupportedSchema}
	}
	return BundleSource{Bundle: b, Status: BundleOK}
}

// ShadowOutcome is the Phase-1 comparison for one ref — for logging only.
type ShadowOutcome struct {
	Evaluated     bool // local evaluation actually ran (false for non-target refs)
	LocalAllowed  bool
	RemoteAllowed bool
	Mismatch      bool            // LocalAllowed != RemoteAllowed
	Violations    []RuleViolation // local blocking violations, to name the ruleset/rule in the log
}

// ShadowCompareRef runs local evaluation for one ref and compares it to the remote
// verdict. remoteDenied is the *Denied the authoritative remote path returned (nil
// = remote allowed). The verdict is NOT changed here — the caller ignores
// LocalAllowed and only logs Mismatch (Phase 1).
func ShadowCompareRef(ctx context.Context, b *Bundle, repoPath, oldHash, newHash, ref string, remoteDenied *Denied) ShadowOutcome {
	res, ran := LocalEvaluateRef(ctx, b, repoPath, oldHash, newHash, ref)
	return compareOutcome(res, ran, remoteDenied == nil)
}

// compareOutcome is the pure comparison, split out so it is unit-testable without
// a git repo (LocalEvaluateRef shells out to git for facts).
func compareOutcome(res BundleEvalResult, ran, remoteAllowed bool) ShadowOutcome {
	if !ran {
		return ShadowOutcome{Evaluated: false, RemoteAllowed: remoteAllowed}
	}
	return ShadowOutcome{
		Evaluated:     true,
		LocalAllowed:  res.Allowed,
		RemoteAllowed: remoteAllowed,
		Mismatch:      res.Allowed != remoteAllowed,
		Violations:    res.Violations,
	}
}

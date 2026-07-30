package ruleguard

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// SupportedBundleSchemaVersion is the only bundle schema this executor knows how
// to evaluate. A bundle stamped with any other version is NOT locally evaluated —
// the caller falls back to the remote path and logs it (design §7.3, §8). This
// mirrors api-service bundle.py BUNDLE_SCHEMA_VERSION; the two MUST move together.
const SupportedBundleSchemaVersion = 1

// maxDecodedBundleBytes caps the gzip-decompressed bundle size. The transport
// already enforces a 32KB *encoded* ceiling (design §5.1); this second cap guards
// the decompressed side so a hostile/corrupt gzip stream can't balloon memory in
// the push choke point. 4MB is far above the ~25KB raw worst case in §4.3.
const maxDecodedBundleBytes = 4 << 20

// Bundle is the push-rule snapshot the gateway hands the executor at auth time so
// the pre-receive hook can evaluate rulesets locally, with zero outbound calls
// (design §4.1). It is produced by api-service apps/rulesets/evaluation/bundle.py
// (build_push_bundle) — this struct MUST stay shape-compatible with that producer.
//
// The executor deliberately knows NOTHING about rule kinds: the compiled
// expression AST is the entire contract, so a new kind never requires an executor
// change (design §6, §7.2). Kind / target_* fields ride along for logging only.
type Bundle struct {
	SchemaVersion   int             `json:"schema_version"`
	RepositoryID    int             `json:"repository_id"`
	DefaultBranch   *string         `json:"default_branch"`    // ~DEFAULT_BRANCH token resolution (§6.5)
	GeneratedAt     string          `json:"generated_at"`      // audit only — staleness tracking (§5.2)
	RefNameSentinel string          `json:"ref_name_sentinel"` // placeholder the executor swaps per ref (§ bundle.py)
	Actor           BundleActor     `json:"actor"`
	Rulesets        []BundleRuleset `json:"rulesets"`
	// LegacyRules carries pre-ruleset WorkspaceRule/RepositoryRule payloads
	// (design §4.1). The current producer (bundle.py build_push_bundle) does not
	// emit these yet; the field is parsed for forward-compatibility but is not
	// evaluated — see EvaluateBundle. When the producer starts emitting them their
	// aggregation semantics must be defined jointly with api-service.
	LegacyRules []BundleRule `json:"legacy_rules"`
}

// BundleActor is the push actor with role vocabulary already collapsed to integer
// ranks by the server (design §4.2). The executor never sees role strings
// (read/write/manage, member/owner) — it only injects these ranks as the
// actor.*_role_rank facts. role_rank folds unknown/None/blank → 0 (fail-closed);
// that meaning is preserved by shipping the rank, not the string.
type BundleActor struct {
	ID                *string `json:"id"` // user uuid — audit log only, never evaluated
	RepoRoleRank      int     `json:"repo_role_rank"`
	WorkspaceRoleRank int     `json:"workspace_role_rank"`
}

// BundleRuleset is one applicable ruleset. ref_include/ref_exclude/target_type are
// shipped raw because ref applicability cannot be pre-folded at auth time (the
// pushed ref is unknown then) — the executor decides it per ref (§6.5). Bypassed
// is pre-computed by the server (§4.3, §6.4): the executor implements no actor
// matching.
type BundleRuleset struct {
	ID          int          `json:"id"`
	Name        string       `json:"name"`
	Level       string       `json:"level"`       // workspace | repository — logging/audit
	Enforcement string       `json:"enforcement"` // active | evaluate (disabled already excluded)
	TargetType  string       `json:"target_type"` // branch | tag | push
	RefInclude  []string     `json:"ref_include"` // empty = all refs
	RefExclude  []string     `json:"ref_exclude"` // exclude wins over include
	Bypassed    bool         `json:"bypassed"`    // server-computed; still evaluated for audit (§6.3)
	Rules       []BundleRule `json:"rules"`
}

// BundleRule is a single compiled deny rule. ExpressionAST is the ONLY field the
// evaluator reads; Kind/TargetType/TargetValue are for the human-facing denial
// reason and logs. Granted is a server-pre-folded identity grant (RuleActor): a
// granted rule never counts as a violation (mirrors evaluator.py _actor_granted).
type BundleRule struct {
	ID            int            `json:"id"`
	Kind          string         `json:"kind"` // reason text / logging only — never evaluated
	TargetType    string         `json:"target_type"`
	TargetValue   string         `json:"target_value"`
	FailPolicy    string         `json:"fail_policy"` // open | closed (§6.7)
	Granted       bool           `json:"granted"`     // server-computed identity grant → not a violation
	ExpressionAST map[string]any `json:"expression_ast"`
}

// ParseBundle unmarshals a raw (already-decoded) JSON bundle. Callers that receive
// the gzip+base64 transport form use DecodeBundle instead.
func ParseBundle(raw []byte) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("bundle unmarshal: %w", err)
	}
	return &b, nil
}

// DecodeBundle reverses the gateway's transport encoding: base64 → gzip →
// JSON (design §5.1). The AST JSON repeats keys heavily, so gzip compresses it to
// a few percent, keeping the encoded form well under the 32KB transport ceiling.
//
// The base64 alphabet MUST match the gateway producer (git-ssh handshake token /
// git-http X-Rule-Bundle header — tasks 4/5). We use standard base64 with padding;
// if those gateways settle on a different alphabet this decoder must follow.
func DecodeBundle(encoded string) (*Bundle, error) {
	if encoded == "" {
		return nil, fmt.Errorf("bundle: empty payload")
	}
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("bundle base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("bundle gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()

	raw, err := io.ReadAll(io.LimitReader(zr, maxDecodedBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("bundle inflate: %w", err)
	}
	if len(raw) > maxDecodedBundleBytes {
		return nil, fmt.Errorf("bundle: decoded size exceeds %d bytes", maxDecodedBundleBytes)
	}
	return ParseBundle(raw)
}

// SchemaSupported reports whether this executor can evaluate the bundle's schema
// version. An unsupported version means "fall back to remote + log" (§8), never
// "silently mis-evaluate".
func (b *Bundle) SchemaSupported() bool {
	return b != nil && b.SchemaVersion == SupportedBundleSchemaVersion
}

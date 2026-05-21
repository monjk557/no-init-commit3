package config

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// deprecationWarned tracks which legacy env keys have already emitted a warning,
// so the message is printed at most once per process even if the helper is
// invoked multiple times (e.g. config reload in tests).
var (
	deprecationWarned   = make(map[string]struct{})
	deprecationWarnedMu sync.Mutex
)

// envWithAlias resolves an environment variable that has been renamed.
//
// Resolution order:
//  1. If newKey is set (non-empty), return it.
//  2. Otherwise, walk legacyKeys in order; the first non-empty match is
//     returned and a one-shot structured deprecation warning is emitted to
//     stderr (event=env_var_deprecated, JSON line — consistent with the
//     production log encoder used elsewhere in the app).
//  3. If neither is set, return "" — the caller decides between a default
//     value and a fail-fast error (preferring fail-fast on the *new* key).
//
// Pattern follows doc-360 §7 Phase 1 (CR 4254 service-pipeline,
// collabops-api-piper CR 4303 + worktree-2972-env-alias). Legacy fallback is
// removed in Phase 3 once helm-charts/values catch up.
func envWithAlias(newKey string, legacyKeys ...string) string {
	if v, ok := os.LookupEnv(newKey); ok && v != "" {
		return v
	}
	for _, legacy := range legacyKeys {
		if v, ok := os.LookupEnv(legacy); ok && v != "" {
			warnDeprecated(legacy, newKey)
			return v
		}
	}
	return ""
}

// warnDeprecated emits a structured one-shot warning to stderr.
//
// Output is a single JSON line so log aggregators (Loki/Splunk) can index it
// the same way the runtime logger records are indexed. We cannot take a
// logger dependency here because config.Load() runs before the logger is
// initialised — that would invert the bootstrap order.
func warnDeprecated(legacyKey, newKey string) {
	deprecationWarnedMu.Lock()
	defer deprecationWarnedMu.Unlock()
	if _, seen := deprecationWarned[legacyKey]; seen {
		return
	}
	deprecationWarned[legacyKey] = struct{}{}
	fmt.Fprintf(
		os.Stderr,
		`{"level":"warn","ts":%q,"event":"env_var_deprecated","deprecated_key":%q,"use_instead":%q,"doc":"doc-360-§7"}`+"\n",
		time.Now().UTC().Format(time.RFC3339),
		legacyKey,
		newKey,
	)
}

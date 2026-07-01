package protocol

import (
	"os"
	"strconv"
	"strings"
)

// pack_limits.go bounds the memory a single `git upload-pack` (and the
// `git pack-objects` child it spawns) may consume, so that a burst of
// concurrent clones cannot sum past the container memory limit and trip a
// cgroup OOM that kills the whole pod — severing every co-tenant session.
//
// git propagates GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n to
// child processes, so injecting these on the upload-pack command also bounds
// the pack-objects child that actually builds the pack. See Issue-5071 and
// Doc-635 (design) for the rationale and the GitLab/Gitaly reference.
//
// Defaults are conservative and can be overridden per environment. Setting a
// value to an empty string, "0", or "off" omits that single knob; leaving all
// three unset applies the built-in defaults.
const (
	// EnvPackWindowMemory caps the memory of git's delta-search window per
	// thread (git config pack.windowMemory). Bounds the dominant allocation
	// in pack generation.
	EnvPackWindowMemory = "GIT_EXECUTOR_PACK_WINDOW_MEMORY"
	// EnvPackThreads caps pack-objects delta-compression threads
	// (git config pack.threads). Each thread holds its own window, so total
	// peak RSS scales with this number — keep it small on memory-bound pods.
	EnvPackThreads = "GIT_EXECUTOR_PACK_THREADS"
	// EnvPackDeltaCacheSize caps the delta cache (git config pack.deltaCacheSize).
	EnvPackDeltaCacheSize = "GIT_EXECUTOR_PACK_DELTA_CACHE_SIZE"

	// EnvPackAllowReuse / EnvPackUseBitmaps pin the git defaults that let
	// upload-pack serve clones by reusing existing (bitmapped) pack bytes
	// verbatim instead of re-traversing + re-deltaing the object graph — the
	// low-memory path (see Issue-5071). These are ON by default in git ≥2.31,
	// so pinning them here changes nothing today; it is a belt-and-suspenders
	// guard against a future git default flip or a global config override
	// silently disabling pack reuse. Reuse only kicks in once bitmaps exist
	// (maintenance atomic-bitmap fix).
	EnvPackAllowReuse = "GIT_EXECUTOR_PACK_ALLOW_REUSE"
	EnvPackUseBitmaps = "GIT_EXECUTOR_PACK_USE_BITMAPS"

	// Defaults sized for a memory-bound pod (e.g. 2Gi limit): a single
	// pack-objects stays roughly within windowMemory + deltaCacheSize + base
	// (~200MB), so a handful of concurrent clones (bounded by
	// MaxConcurrentClones) fit the budget with headroom. threads=1 trades
	// delta-compression parallelism for a hard per-op RSS ceiling — the safe
	// default; raise GIT_EXECUTOR_PACK_THREADS on higher-memory pods.
	defaultPackWindowMemory   = "128m"
	defaultPackThreads        = "1"
	defaultPackDeltaCacheSize = "64m"

	defaultPackAllowReuse = "true"
	defaultPackUseBitmaps = "true"
)

// packLimitEnv builds the GIT_CONFIG_* environment entries that bound
// per-operation pack memory. It returns nil when every knob is explicitly
// disabled, in which case callers append nothing and git uses its own
// defaults (unbounded).
func packLimitEnv() []string {
	type kv struct{ key, value string }

	pairs := make([]kv, 0, 3)
	if v := packLimitValue(EnvPackWindowMemory, defaultPackWindowMemory); v != "" {
		pairs = append(pairs, kv{"pack.windowMemory", v})
	}
	if v := packLimitValue(EnvPackThreads, defaultPackThreads); v != "" {
		pairs = append(pairs, kv{"pack.threads", v})
	}
	if v := packLimitValue(EnvPackDeltaCacheSize, defaultPackDeltaCacheSize); v != "" {
		pairs = append(pairs, kv{"pack.deltaCacheSize", v})
	}
	if v := packLimitValue(EnvPackAllowReuse, defaultPackAllowReuse); v != "" {
		pairs = append(pairs, kv{"pack.allowPackReuse", v})
	}
	if v := packLimitValue(EnvPackUseBitmaps, defaultPackUseBitmaps); v != "" {
		pairs = append(pairs, kv{"pack.useBitmaps", v})
	}
	if len(pairs) == 0 {
		return nil
	}

	env := make([]string, 0, len(pairs)*2+1)
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))
	for i, p := range pairs {
		idx := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+idx+"="+p.key)
		env = append(env, "GIT_CONFIG_VALUE_"+idx+"="+p.value)
	}
	return env
}

// packLimitValue resolves one knob: an unset env falls back to def; an
// explicit "", "0", or "off" (case-insensitive) disables it (returns "").
func packLimitValue(envKey, def string) string {
	raw, ok := os.LookupEnv(envKey)
	if !ok {
		return def
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" || strings.EqualFold(raw, "off") {
		return ""
	}
	return raw
}

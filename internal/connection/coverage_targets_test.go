package connection

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

const zeroHash40 = "0000000000000000000000000000000000000000"

// originalPATH is captured at package-init time, before any test body runs.
// A sibling test in this package calls os.Clearenv() (coverage_extra_test.go)
// and never restores PATH, which would otherwise make exec.LookPath("git")
// fail for git-dependent tests regardless of execution order.
var originalPATH = os.Getenv("PATH")

// ensureGitPATH restores a working PATH for the duration of the test if a
// sibling test has wiped the process environment. t.Setenv reverts it afterward.
func ensureGitPATH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil && originalPATH != "" {
		t.Setenv("PATH", originalPATH)
	}
}

// stubProgressTracker is a controllable ProgressTracker for timeout tests.
type stubProgressTracker struct {
	stalled bool
}

func (s *stubProgressTracker) RecordProgress(int64)     {}
func (s *stubProgressTracker) IsStalled() bool          { return s.stalled }
func (s *stubProgressTracker) SetPackObjectsPhase(bool) {}
func (s *stubProgressTracker) GetProgress() (int64, float64, time.Duration) {
	return 0, 0, 0
}

// --- progress_aware.go: SetPushContext / GetPushContext ---------------------

func TestProgressAwareConnection_PushContext(t *testing.T) {
	conn := NewProgressAwareConnection(&MockTCPConn{}, testTimeoutPolicy(), zaptest.NewLogger(t))
	defer conn.Close()

	// Defaults are empty before being set.
	repoID, ws, tok := conn.GetPushContext()
	assert.Empty(t, repoID)
	assert.Empty(t, ws)
	assert.Empty(t, tok)

	conn.SetPushContext("repo-123", "acme", "decision-token-xyz")

	repoID, ws, tok = conn.GetPushContext()
	assert.Equal(t, "repo-123", repoID)
	assert.Equal(t, "acme", ws)
	assert.Equal(t, "decision-token-xyz", tok)
}

// --- progress_aware.go: SetRuleBundle / GetRuleBundle -----------------------

func TestProgressAwareConnection_RuleBundle(t *testing.T) {
	conn := NewProgressAwareConnection(&MockTCPConn{}, testTimeoutPolicy(), zaptest.NewLogger(t))
	defer conn.Close()

	// Defaults: no bundle, no overflow.
	b, overflow := conn.GetRuleBundle()
	assert.Empty(t, b)
	assert.False(t, overflow)

	// Normal bundle.
	conn.SetRuleBundle("b64gz-encoded", false)
	b, overflow = conn.GetRuleBundle()
	assert.Equal(t, "b64gz-encoded", b)
	assert.False(t, overflow)

	// Overflow signal (gateway dropped an over-ceiling bundle).
	conn.SetRuleBundle("", true)
	b, overflow = conn.GetRuleBundle()
	assert.Empty(t, b)
	assert.True(t, overflow)
}

// --- io_wrapper.go: NewIOWrapper branches -----------------------------------

func TestNewIOWrapper_ConfigBranches(t *testing.T) {
	t.Run("nil config uses defaults", func(t *testing.T) {
		iw := NewIOWrapper(&MockTCPConn{}, nil)
		require.NotNil(t, iw)
		assert.NotNil(t, iw.GetReader())
		assert.NotNil(t, iw.GetWriter())
	})

	t.Run("zero buffer sizes fall back to unsized bufio", func(t *testing.T) {
		iw := NewIOWrapper(&MockTCPConn{}, &IOWrapperConfig{
			ReadBufferSize:  0,
			WriteBufferSize: 0,
		})
		require.NotNil(t, iw)
		assert.NotNil(t, iw.GetReader())
		assert.NotNil(t, iw.GetWriter())
	})

	t.Run("explicit positive buffer sizes", func(t *testing.T) {
		iw := NewIOWrapper(&MockTCPConn{}, &IOWrapperConfig{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		})
		require.NotNil(t, iw)
		assert.NotNil(t, iw.GetReader())
		assert.NotNil(t, iw.GetWriter())
	})
}

// --- timeout_manager.go: NewTimeoutManager defaults -------------------------

func TestNewTimeoutManager_NilPolicyAndLogger(t *testing.T) {
	// Both Policy and Logger nil => defaults applied without panicking.
	tm := NewTimeoutManager(nil, &TimeoutManagerConfig{})
	defer tm.Stop()

	assert.Equal(t, PhaseHandshake, tm.GetCurrentPhase())
	// A default policy means a within-limits check on a fresh manager passes.
	require.NoError(t, tm.IsWithinLimits(context.Background()))
}

// --- timeout_manager.go: IsWithinLimits phase/stop/stall/ctx ----------------

func newQuietTimeoutManager(t *testing.T) *timeoutManager {
	t.Helper()
	policy := testTimeoutPolicy()
	// Very long check interval so the background monitor stays out of the way.
	policy.ProgressCheckInterval = time.Hour
	tm := NewTimeoutManager(nil, &TimeoutManagerConfig{
		Policy: policy,
		Logger: zap.NewNop(),
	}).(*timeoutManager)
	return tm
}

func (tm *timeoutManager) forcePhase(phase ConnectionPhase, startedAgo time.Duration) {
	atomic.StoreInt32(&tm.currentPhase, int32(phase))
	tm.mu.Lock()
	tm.operationStart = time.Now().Add(-startedAgo)
	tm.mu.Unlock()
}

func TestTimeoutManager_IsWithinLimits(t *testing.T) {
	t.Run("handshake exceeded", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.forcePhase(PhaseHandshake, time.Hour)
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "handshake")
	})

	t.Run("auth exceeded", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.forcePhase(PhaseAuth, time.Hour)
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "auth")
	})

	t.Run("protocol negotiation exceeded", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.forcePhase(PhaseProtocolNegotiation, time.Hour)
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "protocol")
	})

	t.Run("operation stalled", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.progressTracker = &stubProgressTracker{stalled: true}
		tm.forcePhase(PhaseOperation, time.Second)
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stall")
	})

	t.Run("operation max timeout without tracker", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.progressTracker = nil
		tm.forcePhase(PhaseOperation, 24*time.Hour)
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max")
	})

	t.Run("within limits but context cancelled", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.forcePhase(PhaseHandshake, 0) // well within limits
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := tm.IsWithinLimits(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("stopped manager reports closed", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		tm.Stop()
		err := tm.IsWithinLimits(context.Background())
		require.Error(t, err)
	})

	t.Run("fresh handshake within limits", func(t *testing.T) {
		tm := newQuietTimeoutManager(t)
		defer tm.Stop()
		tm.forcePhase(PhaseHandshake, 0)
		require.NoError(t, tm.IsWithinLimits(context.Background()))
	})
}

// --- concurrency_control.go: must -------------------------------------------

func TestMust(t *testing.T) {
	t.Run("returns value on nil error", func(t *testing.T) {
		assert.Equal(t, 42, must(42, nil))
	})

	t.Run("panics on error", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "must should panic on error")
		}()
		_ = must(0, errors.New("boom"))
		t.Fatal("must did not panic")
	})
}

// --- atomic_push.go: ExecutePushOperation -----------------------------------

// gitInDir runs a git command in dir and fails the test on error.
func gitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestExecutePushOperation(t *testing.T) {
	ensureGitPATH(t)
	logger := zaptest.NewLogger(t)

	t.Run("no prepared updates is a no-op", func(t *testing.T) {
		handler := NewAtomicPushHandler(t.TempDir(), "op-empty", logger)
		require.NoError(t, handler.ExecutePushOperation())
	})

	t.Run("creates ref against a real repository", func(t *testing.T) {
		repo := t.TempDir()
		gitInDir(t, repo, "init", "-q", "-b", "main")
		gitInDir(t, repo, "commit", "--allow-empty", "-q", "-m", "seed")
		head := gitInDir(t, repo, "rev-parse", "HEAD")

		handler := NewAtomicPushHandler(repo, "op-create", logger)
		// Protocol line format: "<old> <new> <refname>".
		require.NoError(t, handler.PreparePushOperation([]string{
			zeroHash40 + " " + head + " refs/heads/pushed",
		}))
		require.NoError(t, handler.ExecutePushOperation())

		got := gitInDir(t, repo, "rev-parse", "refs/heads/pushed")
		assert.Equal(t, head, got)
	})
}

// --- progress_aware.go: monitorProgress max-timeout branch ------------------

func TestMonitorProgress_MaxTimeoutClosesConnection(t *testing.T) {
	policy := DefaultTimeoutPolicy()
	policy.StallThreshold = time.Hour // never considered stalled
	policy.OperationMaxTimeout = 40 * time.Millisecond
	policy.ProgressCheckInterval = 10 * time.Millisecond

	conn := NewProgressAwareConnection(&MockTCPConn{}, policy, zaptest.NewLogger(t))
	defer conn.Close()

	require.NoError(t, conn.SetPhase(PhaseOperation))

	// The monitor goroutine should trip the absolute max-timeout and Close().
	require.Eventually(t, func() bool {
		return !conn.IsAlive()
	}, 2*time.Second, 10*time.Millisecond, "connection should be closed after max timeout")
}

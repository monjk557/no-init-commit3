//go:build gogit

package merge

import (
	"context"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
)

func Test_NewMergeEnv_SetsNowToCurrentTime(t *testing.T) {
	t.Parallel()

	before := time.Now()
	env := NewMergeEnv(context.Background(), nil, nil, nil)
	after := time.Now()

	if env.Now.Before(before) || env.Now.After(after) {
		t.Fatalf("Now=%v should be between %v and %v", env.Now, before, after)
	}
}

func Test_NewMergeEnvWithTime_SetsSpecificTime(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	env := NewMergeEnvWithTime(context.Background(), nil, fixedTime, nil, nil)

	if !env.Now.Equal(fixedTime) {
		t.Fatalf("Now=%v want=%v", env.Now, fixedTime)
	}
}

func Test_MergeEnv_IsCancelled_ReturnsFalse_WhenNotCancelled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := NewMergeEnv(ctx, nil, nil, nil)

	if env.IsCancelled() {
		t.Fatalf("IsCancelled()=true want=false for non-cancelled context")
	}
}

func Test_MergeEnv_IsCancelled_ReturnsTrue_WhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	env := NewMergeEnv(ctx, nil, nil, nil)

	cancel()

	if !env.IsCancelled() {
		t.Fatalf("IsCancelled()=false want=true for cancelled context")
	}
}

func Test_MergeEnv_StoresRepo(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	repo, err := gogit.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}

	env := NewMergeEnv(context.Background(), repo, nil, nil)

	if env.Repo != repo {
		t.Fatalf("Repo=%v want=%v", env.Repo, repo)
	}
}

func Test_MergeEnv_StoresContext(t *testing.T) {
	t.Parallel()

	type contextKey string
	const testKey contextKey = "key"
	ctx := context.WithValue(context.Background(), testKey, "value")
	env := NewMergeEnv(ctx, nil, nil, nil)

	if env.Ctx != ctx {
		t.Fatalf("Ctx=%v want=%v", env.Ctx, ctx)
	}
}

package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IndexPackResult holds the result of git index-pack.
type IndexPackResult struct {
	PackHash string // the SHA-1 of the resulting pack
}

// IndexPackFromReader indexes a packfile read from the given reader into the repository.
//
// Architecture (two-phase approach):
//
//  1. Save the incoming packfile to a LOCAL temp file (os.TempDir()).
//     CopyPackfileFromStream detects the packfile boundary via incremental SHA-1
//     trailer verification, critical for TCP connections where the stream stays
//     open after the packfile.
//
//  2. Pipe the local temp file into `git index-pack --stdin --fix-thin`.
//     --fix-thin resolves thin pack deltas using objects already in the repo.
//     --stdin is required by git when using --fix-thin.
//     git writes the final pack+idx directly into $GIT_DIR/objects/pack/.
func IndexPackFromReader(ctx context.Context, repoPath string, r io.Reader) (*IndexPackResult, error) {
	// Ensure objects/pack/ directory exists.
	packDir := filepath.Join(repoPath, "objects", "pack")
	if err := os.MkdirAll(packDir, 0755); err != nil {
		return nil, fmt.Errorf("ensure objects/pack dir: %w", err)
	}

	// Phase 1: Save incoming packfile to a LOCAL temp file.
	// CopyPackfileFromStream handles TCP stream boundary detection via SHA-1 trailer.
	tmpFile, err := os.CreateTemp("", "git-index-pack-*.pack")
	if err != nil {
		return nil, fmt.Errorf("create local temp pack file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	n, err := CopyPackfileFromStream(r, tmpFile)
	tmpFile.Close()
	if err != nil {
		return nil, fmt.Errorf("save packfile (%d bytes written): %w", n, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("empty packfile received")
	}

	// Phase 2: Pipe the local temp file into git index-pack --stdin --fix-thin.
	// --stdin is required when using --fix-thin (git enforces this).
	// --stdin writes pack+idx directly to $GIT_DIR/objects/pack/.
	// core.fsync=none: container filesystems often don't support fsync.
	packInput, err := os.Open(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("reopen temp pack file: %w", err)
	}
	defer packInput.Close()

	args := []string{"-c", "core.fsync=none", "index-pack", "--stdin", "--fix-thin"}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	cmd.Stdin = packInput

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrMsg := strings.TrimRight(stderr.String(), "\r\n")
		return nil, fmt.Errorf("git index-pack: %w (stderr: %s)", err, stderrMsg)
	}

	packHash := strings.TrimSpace(stdout.String())
	if packHash == "" {
		return nil, fmt.Errorf("git index-pack returned empty hash")
	}

	return &IndexPackResult{PackHash: packHash}, nil
}

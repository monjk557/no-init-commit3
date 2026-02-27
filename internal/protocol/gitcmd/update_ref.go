package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// RefUpdateCmd represents a single reference update in a transaction.
type RefUpdateCmd struct {
	RefName string
	OldOID  string // zero hash for create, expected current for update/delete
	NewOID  string // zero hash for delete
}

// AtomicRefUpdate runs `git update-ref --stdin -z` with a transaction protocol:
// start → update/create/delete → prepare → commit
// This provides kernel-level CAS (Compare-And-Swap) with automatic .lock file management.
func AtomicRefUpdate(ctx context.Context, repoPath string, updates []RefUpdateCmd) error {
	if len(updates) == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, "git", "update-ref", "--stdin", "-z")
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start git update-ref: %w", err)
	}

	// Write NUL-terminated fields to stdin.
	// With -z flag, git update-ref uses NUL as the line terminator.
	// Format: "command SP refname\0value1\0value2\0" for operations,
	//         "keyword\0" for transaction keywords (start, prepare, commit).
	writeField := func(w io.Writer, s string) error {
		if _, err := w.Write([]byte(s)); err != nil {
			return err
		}
		_, err := w.Write([]byte{0})
		return err
	}

	// Start transaction
	if err := writeField(stdin, "start"); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("write start: %w", err)
	}

	zeroHash := "0000000000000000000000000000000000000000"
	for _, u := range updates {
		isCreate := u.OldOID == zeroHash || u.OldOID == ""
		isDelete := u.NewOID == zeroHash

		switch {
		case isDelete:
			// -z format: "delete SP <ref>\0<oldvalue>\0"
			if err := writeField(stdin, "delete "+u.RefName); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write delete %s: %w", u.RefName, err)
			}
			if err := writeField(stdin, u.OldOID); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write delete oldoid %s: %w", u.RefName, err)
			}
		case isCreate:
			// -z format: "create SP <ref>\0<newvalue>\0"
			if err := writeField(stdin, "create "+u.RefName); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write create %s: %w", u.RefName, err)
			}
			if err := writeField(stdin, u.NewOID); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write create newoid %s: %w", u.RefName, err)
			}
		default:
			// -z format: "update SP <ref>\0<newvalue>\0<oldvalue>\0"
			if err := writeField(stdin, "update "+u.RefName); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write update %s: %w", u.RefName, err)
			}
			if err := writeField(stdin, u.NewOID); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write update newoid %s: %w", u.RefName, err)
			}
			if err := writeField(stdin, u.OldOID); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write update oldoid %s: %w", u.RefName, err)
			}
		}
	}

	// Prepare (validate all refs, acquire locks)
	if err := writeField(stdin, "prepare"); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("write prepare: %w", err)
	}

	// Commit (apply atomically)
	if err := writeField(stdin, "commit"); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("write commit: %w", err)
	}

	if err := stdin.Close(); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("close stdin: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git update-ref: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// VerifyRef returns the OID of a given ref, or error if the ref doesn't exist.
func VerifyRef(ctx context.Context, repoPath string, refName string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", refName)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --verify %s: %w (stderr: %s)", refName, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

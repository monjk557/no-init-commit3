package gitutil

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

const (
	// MaxOutputSmall caps commands that produce small output (rev-parse, symbolic-ref, cat-file -t).
	MaxOutputSmall int64 = 1 * 1024 * 1024 // 1 MB

	// MaxOutputMedium caps commands with moderate output (log, for-each-ref, rev-list).
	MaxOutputMedium int64 = 10 * 1024 * 1024 // 10 MB

	// MaxOutputLarge caps commands with potentially large output (ls-tree -r, diff-tree, show blob).
	MaxOutputLarge int64 = 50 * 1024 * 1024 // 50 MB

	// DefaultMaxOutput is used when no specific limit is needed.
	DefaultMaxOutput = MaxOutputMedium
)

// limitedBuffer is a bytes.Buffer that stops growing after maxBytes.
// Excess data is silently discarded to prevent OOM, while still allowing
// the source process to run to completion (pipe stays drained).
type limitedBuffer struct {
	buf      bytes.Buffer
	maxBytes int64
	overflow bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.overflow {
		return len(p), nil // discard excess, keep pipe drained
	}
	if int64(lb.buf.Len())+int64(len(p)) > lb.maxBytes {
		lb.overflow = true
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) Bytes() []byte {
	return lb.buf.Bytes()
}

// OutputWithLimit is a drop-in replacement for cmd.Output() that caps stdout
// at maxBytes. If output exceeds the limit, the data is discarded and an error
// is returned after the process exits.
//
// cmd.Stderr may be set before calling — it works identically to cmd.Output().
// Callers only need to change:
//
//	out, err := cmd.Output()            →  out, err := gitutil.OutputWithLimit(cmd, limit)
func OutputWithLimit(cmd *exec.Cmd, maxBytes int64) ([]byte, error) {
	lb := &limitedBuffer{maxBytes: maxBytes}
	cmd.Stdout = lb

	err := cmd.Run()

	if lb.overflow {
		return nil, fmt.Errorf("git output exceeded limit of %d bytes", maxBytes)
	}
	if err != nil {
		return lb.Bytes(), err
	}
	return lb.Bytes(), nil
}

// CombinedOutputWithLimit is a drop-in replacement for cmd.CombinedOutput()
// that caps the merged stdout+stderr at maxBytes.
//
// Neither cmd.Stdout nor cmd.Stderr should be set before calling.
func CombinedOutputWithLimit(cmd *exec.Cmd, maxBytes int64) ([]byte, error) {
	lb := &limitedBuffer{maxBytes: maxBytes}
	cmd.Stdout = lb
	cmd.Stderr = lb

	err := cmd.Run()

	if lb.overflow {
		return nil, fmt.Errorf("git output exceeded limit of %d bytes", maxBytes)
	}
	if err != nil {
		return lb.Bytes(), err
	}
	return lb.Bytes(), nil
}

// MaxRequestBodyBytes is the limit for incoming Git protocol request bodies
// (upload-pack negotiation, etc.). Negotiation payloads are typically < 1 MB.
const MaxRequestBodyBytes int64 = 10 * 1024 * 1024 // 10 MB

// LimitReadCloser wraps an io.ReadCloser with an io.LimitReader while preserving Close.
func LimitReadCloser(rc io.ReadCloser, maxBytes int64) io.ReadCloser {
	return &limitedReadCloser{
		Reader: io.LimitReader(rc, maxBytes),
		Closer: rc,
	}
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

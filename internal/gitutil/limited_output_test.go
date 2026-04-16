package gitutil

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestOutputWithLimit_SmallOutput(t *testing.T) {
	cmd := exec.Command("echo", "hello world")
	out, err := OutputWithLimit(cmd, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestOutputWithLimit_ExactlyAtLimit(t *testing.T) {
	// Generate exactly 64 bytes of output
	cmd := exec.Command("head", "-c", "64", "/dev/zero")
	out, err := OutputWithLimit(cmd, 64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 64 {
		t.Errorf("got %d bytes, want 64", len(out))
	}
}

func TestOutputWithLimit_ExceedsLimit(t *testing.T) {
	// Generate 1024 bytes but limit to 512
	cmd := exec.Command("head", "-c", "1024", "/dev/zero")
	out, err := OutputWithLimit(cmd, 512)
	if err == nil {
		t.Fatal("expected error for output exceeding limit, got nil")
	}
	if out != nil {
		t.Errorf("expected nil output, got %d bytes", len(out))
	}
	if !strings.Contains(err.Error(), "exceeded limit") {
		t.Errorf("error %q does not contain 'exceeded limit'", err.Error())
	}
}

func TestOutputWithLimit_CommandFails(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo partial; exit 1")
	out, err := OutputWithLimit(cmd, 1024)
	if err == nil {
		t.Fatal("expected error for failed command, got nil")
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Errorf("expected *exec.ExitError, got %T", err)
	}
	if !strings.Contains(string(out), "partial") {
		t.Errorf("expected partial output containing 'partial', got %q", string(out))
	}
}

func TestOutputWithLimit_StderrCapture(t *testing.T) {
	var stderr bytes.Buffer
	cmd := exec.Command("sh", "-c", "echo stdout_data; echo stderr_data >&2")
	cmd.Stderr = &stderr

	out, err := OutputWithLimit(cmd, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "stdout_data") {
		t.Errorf("stdout output %q does not contain 'stdout_data'", string(out))
	}
	if !strings.Contains(stderr.String(), "stderr_data") {
		t.Errorf("stderr %q does not contain 'stderr_data'", stderr.String())
	}
}

func TestCombinedOutputWithLimit_SmallOutput(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo out; echo err >&2")
	out, err := CombinedOutputWithLimit(cmd, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("combined output %q missing stdout or stderr", got)
	}
}

func TestCombinedOutputWithLimit_ExceedsLimit(t *testing.T) {
	cmd := exec.Command("head", "-c", "1024", "/dev/zero")
	out, err := CombinedOutputWithLimit(cmd, 512)
	if err == nil {
		t.Fatal("expected error for output exceeding limit, got nil")
	}
	if out != nil {
		t.Errorf("expected nil output, got %d bytes", len(out))
	}
	if !strings.Contains(err.Error(), "exceeded limit") {
		t.Errorf("error %q does not contain 'exceeded limit'", err.Error())
	}
}

func TestLimitReadCloser_WithinLimit(t *testing.T) {
	data := []byte("hello world")
	rc := io.NopCloser(bytes.NewReader(data))
	limited := LimitReadCloser(rc, 1024)

	got, err := io.ReadAll(limited)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", string(got), "hello world")
	}
}

func TestLimitReadCloser_BeyondLimit(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	rc := io.NopCloser(bytes.NewReader(data))
	limited := LimitReadCloser(rc, 50)

	got, err := io.ReadAll(limited)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 50 {
		t.Errorf("got %d bytes, want 50", len(got))
	}
	if !bytes.Equal(got, data[:50]) {
		t.Error("truncated data does not match expected prefix")
	}
}

func TestLimitReadCloser_ClosePropagates(t *testing.T) {
	closed := false
	rc := &trackingCloser{
		Reader:  bytes.NewReader([]byte("data")),
		onClose: func() { closed = true },
	}
	limited := LimitReadCloser(rc, 1024)

	if err := limited.Close(); err != nil {
		t.Fatalf("unexpected error on Close: %v", err)
	}
	if !closed {
		t.Error("Close was not propagated to underlying reader")
	}
}

// trackingCloser is a ReadCloser that records whether Close was called.
type trackingCloser struct {
	io.Reader
	onClose func()
}

func (tc *trackingCloser) Close() error {
	tc.onClose()
	return nil
}

package gitcmd

import (
	"bytes"
	"testing"
	"time"
)

func TestMeteredWriter_CountsBytesAndFirstByte(t *testing.T) {
	var buf bytes.Buffer
	mw := NewMeteredWriter(&buf)

	if mw.Bytes() != 0 || !mw.FirstByteAt().IsZero() {
		t.Fatalf("expected zero-value writer, got bytes=%d first=%v", mw.Bytes(), mw.FirstByteAt())
	}

	// Empty write must not set the first-byte timestamp.
	if _, err := mw.Write(nil); err != nil {
		t.Fatalf("write nil: %v", err)
	}
	if !mw.FirstByteAt().IsZero() {
		t.Fatal("empty write should not set first-byte timestamp")
	}

	if _, err := mw.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := mw.FirstByteAt()
	if first.IsZero() {
		t.Fatal("first-byte timestamp not set after non-empty write")
	}
	if _, err := mw.Write([]byte(" world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !mw.FirstByteAt().Equal(first) {
		t.Fatal("first-byte timestamp must not change on subsequent writes")
	}
	if got := mw.Bytes(); got != 11 {
		t.Fatalf("bytes = %d, want 11", got)
	}
	if buf.String() != "hello world" {
		t.Fatalf("underlying = %q", buf.String())
	}
}

func TestMeteredReader_CountsBytes(t *testing.T) {
	mr := NewMeteredReader(bytes.NewReader([]byte("abcdef")))
	out := make([]byte, 4)
	n, err := mr.Read(out)
	if err != nil || n != 4 {
		t.Fatalf("read1: n=%d err=%v", n, err)
	}
	_, _ = mr.Read(out)
	if got := mr.Bytes(); got != 6 {
		t.Fatalf("bytes = %d, want 6", got)
	}
}

func TestCPUTimeNilProcessState(t *testing.T) {
	if got := CPUTime(nil); got != 0 {
		t.Fatalf("CPUTime(nil) = %v, want 0", got)
	}
}

func TestTTFB(t *testing.T) {
	start := time.Unix(1000, 0)
	if got := TTFB(start, time.Time{}); got != 0 {
		t.Fatalf("TTFB with zero first = %v, want 0", got)
	}
	if got := TTFB(start, start.Add(250*time.Millisecond)); got != 250*time.Millisecond {
		t.Fatalf("TTFB = %v, want 250ms", got)
	}
}

func TestParsePackObjectsReused(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"Total 100 (delta 40), reused 80 (delta 30)", 80},
		{"Total 100 (delta 40), reused 0 (delta 0)", 0},
		{"Counting objects: 100", -1},
		{"Total 100", -1},
		{"Total 100 (delta 40), reused notanumber (delta 0)", -1},
	}
	for _, tc := range tests {
		if got := parsePackObjectsReused(tc.line); got != tc.want {
			t.Errorf("parsePackObjectsReused(%q) = %d, want %d", tc.line, got, tc.want)
		}
	}
}

func TestParseIndexPackObjects(t *testing.T) {
	tests := []struct {
		stderr string
		want   int
	}{
		{"Indexing objects: 100% (1234/1234)\nResolving deltas: 100% (500/500)\n", 1234},
		{"Receiving objects: 100% (10/10)", 0},
		{"", 0},
		{"Indexing objects: 50% (no-slash-here", 0},
	}
	for _, tc := range tests {
		if got := parseIndexPackObjects(tc.stderr); got != tc.want {
			t.Errorf("parseIndexPackObjects(%q) = %d, want %d", tc.stderr, got, tc.want)
		}
	}
}

func TestTransferRecorderSeam(t *testing.T) {
	prev := transferRecorder
	t.Cleanup(func() { transferRecorder = prev })

	// nil recorder must be a safe no-op.
	transferRecorder = nil
	RecordTransfer(TransferStats{Operation: "upload-pack"})

	var got []TransferStats
	SetTransferMetricsRecorder(recorderFunc(func(s TransferStats) { got = append(got, s) }))
	RecordTransfer(TransferStats{Operation: "receive-pack", Transport: "ssh", Bytes: 42})

	if len(got) != 1 || got[0].Operation != "receive-pack" || got[0].Bytes != 42 {
		t.Fatalf("recorder did not receive expected stats: %+v", got)
	}
}

type recorderFunc func(TransferStats)

func (f recorderFunc) RecordTransfer(s TransferStats) { f(s) }

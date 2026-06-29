package gitcmd

import (
	"io"
	"os"
	"time"
)

// TransferStats holds network-independent server-processing metrics for a single
// git transfer subprocess (upload-pack pack generation or receive-pack indexing).
//
// CPUTime is the key network-independent figure: the git subprocess's user+system
// CPU time does not grow when a slow client throttles the wire. WallTime, by
// contrast, includes network back-pressure (download) or read stalls (upload);
// (WallTime - CPUTime) therefore approximates time the server spent blocked on
// client I/O. Bytes/Objects/Reused are pure work-volume counters, also unaffected
// by transfer speed.
type TransferStats struct {
	Operation string // "upload-pack" (download) | "receive-pack" (upload)
	Transport string // "http" | "ssh" | "tcp"
	CPUTime   time.Duration
	WallTime  time.Duration
	TTFB      time.Duration // start -> first output byte (download only; 0 otherwise)
	Bytes     int64         // packfile bytes generated (download) or received (upload)
	Objects   int           // objects packed (download) or indexed (upload); 0 if unknown
	Reused    int           // delta objects reused (download only)
	Success   bool
}

// TransferMetricsRecorder receives transfer stats. It is implemented in the
// observability package; gitcmd defines the interface here and never imports
// observability, preserving the layering (low-level git commands stay
// dependency-light and unit-testable).
type TransferMetricsRecorder interface {
	RecordTransfer(stats TransferStats)
}

var transferRecorder TransferMetricsRecorder

// SetTransferMetricsRecorder installs the process-wide transfer metrics recorder.
// It is called once at startup; a nil recorder disables recording.
func SetTransferMetricsRecorder(r TransferMetricsRecorder) { transferRecorder = r }

// RecordTransfer forwards stats to the installed recorder, if any. It is exported
// so transports that spawn git directly (e.g. the TCP daemon's native
// receive-pack) can report through the same seam without re-implementing it.
func RecordTransfer(stats TransferStats) {
	if r := transferRecorder; r != nil {
		r.RecordTransfer(stats)
	}
}

// CPUTime returns the user+system CPU time of a finished process, or 0 if the
// ProcessState is unavailable (e.g. the process failed to start). On Unix the
// wait4 rusage aggregates reaped children, so a parent like `git upload-pack`
// reports CPU that includes its `git pack-objects` child.
func CPUTime(ps *os.ProcessState) time.Duration {
	if ps == nil {
		return 0
	}
	return ps.UserTime() + ps.SystemTime()
}

// TTFB returns first-byte latency relative to start, or 0 if no byte was written.
func TTFB(start, first time.Time) time.Duration {
	if first.IsZero() {
		return 0
	}
	return first.Sub(start)
}

// MeteredWriter counts bytes written and records the timestamp of the first
// non-empty write (used to derive time-to-first-byte).
type MeteredWriter struct {
	W     io.Writer
	n     int64
	first time.Time
}

// NewMeteredWriter wraps w with byte and first-write-time accounting.
func NewMeteredWriter(w io.Writer) *MeteredWriter { return &MeteredWriter{W: w} }

func (m *MeteredWriter) Write(p []byte) (int, error) {
	if len(p) > 0 && m.first.IsZero() {
		m.first = time.Now()
	}
	n, err := m.W.Write(p)
	m.n += int64(n)
	return n, err
}

// Bytes returns the total bytes written so far.
func (m *MeteredWriter) Bytes() int64 { return m.n }

// FirstByteAt returns the timestamp of the first non-empty write (zero if none).
func (m *MeteredWriter) FirstByteAt() time.Time { return m.first }

// MeteredReader counts bytes read from an underlying reader.
type MeteredReader struct {
	R io.Reader
	n int64
}

// NewMeteredReader wraps r with byte accounting.
func NewMeteredReader(r io.Reader) *MeteredReader { return &MeteredReader{R: r} }

func (m *MeteredReader) Read(p []byte) (int, error) {
	n, err := m.R.Read(p)
	m.n += int64(n)
	return n, err
}

// Bytes returns the total bytes read so far.
func (m *MeteredReader) Bytes() int64 { return m.n }

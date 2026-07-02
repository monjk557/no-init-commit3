package logging

import (
	"net/http"
	"os"
	"testing"
)

// TestMain forces access-log sampling off (rate=1) for the package's tests so
// emission assertions are deterministic; the sampling logic itself is covered
// explicitly below with local rate overrides.
func TestMain(m *testing.M) {
	readSampleRate = 1
	os.Exit(m.Run())
}

func TestIsReadRequest(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/repos/o/r/branches", true},
		{http.MethodHead, "/x", true},
		{http.MethodOptions, "/x", true},
		// clone/fetch over smart-HTTP: POST but read.
		{http.MethodPost, "/o/r/git-upload-pack", true},
		{http.MethodGet, "/o/r/info/refs", true},
		// push is a write.
		{http.MethodPost, "/o/r/git-receive-pack", false},
		// other mutations.
		{http.MethodPost, "/repos/o/r/branches", false},
		{http.MethodPatch, "/repos/o/r/branches/x", false},
		{http.MethodDelete, "/repos/o/r/branches/x", false},
	}
	for _, c := range cases {
		if got := isReadRequest(c.method, c.path); got != c.want {
			t.Errorf("isReadRequest(%s,%s)=%v want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestSampleOutAccessLog_KeepsWritesAndErrors(t *testing.T) {
	// Errors are never sampled out, even for reads with rate 0.
	old := readSampleRate
	readSampleRate = 0 // would drop all successful reads
	defer func() { readSampleRate = old }()

	if sampleOutAccessLog(http.MethodGet, "/repos/o/r/branches", 500) {
		t.Error("5xx read must always be logged")
	}
	if sampleOutAccessLog(http.MethodGet, "/repos/o/r/branches", 404) {
		t.Error("4xx read must always be logged")
	}
	// Writes always logged regardless of rate.
	if sampleOutAccessLog(http.MethodPost, "/o/r/git-receive-pack", 200) {
		t.Error("push (write) must always be logged")
	}
	// Successful read with rate 0 → dropped.
	if !sampleOutAccessLog(http.MethodPost, "/o/r/git-upload-pack", 200) {
		t.Error("successful clone read at rate 0 should be sampled out")
	}
}

func TestSampleOutAccessLog_RateOneKeepsAll(t *testing.T) {
	old := readSampleRate
	readSampleRate = 1
	defer func() { readSampleRate = old }()
	if sampleOutAccessLog(http.MethodGet, "/repos/o/r/branches", 200) {
		t.Error("rate=1 must keep all reads")
	}
}

func TestSampleOutAccessLog_RateApproximates(t *testing.T) {
	old := readSampleRate
	readSampleRate = 0.10
	defer func() { readSampleRate = old }()

	const n = 20000
	kept := 0
	for i := 0; i < n; i++ {
		if !sampleOutAccessLog(http.MethodGet, "/repos/o/r/branches", 200) {
			kept++
		}
	}
	ratio := float64(kept) / n
	// Wide tolerance to avoid flakiness; just confirm it's roughly 10%, not 0/100%.
	if ratio < 0.06 || ratio > 0.14 {
		t.Errorf("kept ratio %.3f, want ~0.10", ratio)
	}
}

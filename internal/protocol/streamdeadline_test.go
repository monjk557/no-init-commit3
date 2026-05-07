package protocol

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamDeadliner_ActiveStreamSurvivesFixedWriteTimeout verifies that
// when the http.Server has a short fixed WriteTimeout, a handler that
// installs StreamDeadliner with a longer idle window can still stream
// past that timeout as long as data is flowing. This is the core
// regression we are fixing — without StreamDeadliner the connection is
// killed mid-stream and the client sees a truncated response.
func TestStreamDeadliner_ActiveStreamSurvivesFixedWriteTimeout(t *testing.T) {
	// Server-level absolute write timeout shorter than the stream we
	// will produce. Without StreamDeadliner this would kill the conn
	// mid-response.
	const serverWriteTimeout = 200 * time.Millisecond
	const streamDuration = 800 * time.Millisecond
	const chunkInterval = 80 * time.Millisecond

	// Idle window comfortably larger than chunkInterval so an actively
	// progressing writer never trips it, but also smaller than the
	// total streamDuration so a truly idle stream would be reaped.
	const idleWindow = 400 * time.Millisecond

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sd := NewStreamDeadliner(w, idleWindow)
		if err := sd.Prime(); err != nil {
			t.Errorf("Prime: %v", err)
			return
		}
		out := NewIdleWriter(w, sd)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		end := time.Now().Add(streamDuration)
		for time.Now().Before(end) {
			if _, err := out.Write([]byte("chunk\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(chunkInterval)
		}
	})

	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = serverWriteTimeout
	ts.Config.ReadTimeout = serverWriteTimeout
	ts.Start()
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// We expect more chunks than the server-level WriteTimeout would
	// have allowed if it had remained absolute. With a 200ms timeout
	// and 80ms interval we'd see ~2 chunks at most before the conn
	// would have been killed. With sliding deadlines we should see
	// closer to streamDuration/chunkInterval ~ 10 chunks.
	if len(body) == 0 {
		t.Fatalf("expected streamed body, got empty")
	}
	chunkCount := len(body) / len("chunk\n")
	if chunkCount < 5 {
		t.Errorf("expected stream to outlast fixed WriteTimeout, got only %d chunks (body=%q)", chunkCount, string(body))
	}
}

// TestStreamDeadliner_BlockedWriteFiresDeadline verifies the safety
// property: when a Write is blocked because the client has stopped
// consuming and idleWindow elapses, the Write returns an error so the
// handler can exit and free its concurrency slot. This is the actual
// reap mechanism — net.Conn deadlines fire on blocked I/O, not on
// idle handlers.
func TestStreamDeadliner_BlockedWriteFiresDeadline(t *testing.T) {
	const idleWindow = 200 * time.Millisecond

	writeReturned := make(chan error, 1)
	bigChunk := make([]byte, 8*1024*1024) // 8 MiB — exceeds typical TCP socket buffer

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sd := NewStreamDeadliner(w, idleWindow)
		if err := sd.Prime(); err != nil {
			t.Errorf("Prime: %v", err)
			writeReturned <- err
			return
		}
		out := NewIdleWriter(w, sd)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// Keep writing until either the client drains us (won't
		// happen — we hijack the client to never read) or the
		// deadline fires on a blocked Write.
		for {
			if _, err := out.Write(bigChunk); err != nil {
				writeReturned <- err
				return
			}
		}
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Use a low-level dialer so we can refuse to drain the response
	// body. Closing without reading would fail the test for the wrong
	// reason — instead, hold the conn open and read nothing.
	conn, err := (&net.Dialer{}).Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case err := <-writeReturned:
		if err == nil {
			t.Errorf("expected blocked write to fail after idle window, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("handler did not exit within 5s — idle deadline did not fire on blocked write")
	}
}

// TestStreamDeadliner_ZeroIdleDisablesDeadline verifies that idle <= 0
// disables deadlines entirely (uncapped streams). Operators who already
// enforce timeouts upstream may opt out via env var.
func TestStreamDeadliner_ZeroIdleDisablesDeadline(t *testing.T) {
	const serverWriteTimeout = 100 * time.Millisecond
	const streamDuration = 400 * time.Millisecond

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sd := NewStreamDeadliner(w, 0)
		if err := sd.Prime(); err != nil {
			t.Errorf("Prime: %v", err)
			return
		}
		out := NewIdleWriter(w, sd)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		end := time.Now().Add(streamDuration)
		for time.Now().Before(end) {
			if _, err := out.Write([]byte("x")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = serverWriteTimeout
	ts.Start()
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// With idle=0 and the server-level fixed WriteTimeout disabled at
	// the conn level, we should see most of the streamed body.
	if len(body) < 4 {
		t.Errorf("expected uncapped stream to deliver many chunks, got %d bytes", len(body))
	}
}

// TestIdleReadCloser_ForwardsRead verifies the wrapper passes data
// through unchanged and respects EOF semantics. The deadline-sliding
// path is symmetric with idleWriter and is exercised in production
// against the real http.Server connection — there is no portable way
// to assert SetReadDeadline calls without coupling to http internals.
func TestIdleReadCloser_ForwardsRead(t *testing.T) {
	src := io.NopCloser(strings.NewReader("hello world"))
	sd := NewStreamDeadliner(&deadlineRecorder{}, 1*time.Second)

	rc := NewIdleReadCloser(src, sd)
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("body = %q, want %q", string(got), "hello world")
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestStreamDeadliner_NilReturnsBareWriter verifies the wrappers are
// no-ops when given a nil deadliner — useful for callers that fall
// back gracefully when deadline installation fails.
func TestStreamDeadliner_NilReturnsBareWriter(t *testing.T) {
	w := io.Discard
	if NewIdleWriter(w, nil) != w {
		t.Errorf("NewIdleWriter(_, nil) should return the bare writer")
	}
	r := io.NopCloser(strings.NewReader(""))
	if NewIdleReadCloser(r, nil) != r {
		t.Errorf("NewIdleReadCloser(_, nil) should return the bare reader")
	}
}

// deadlineRecorder is a minimal http.ResponseWriter used to give
// http.NewResponseController a target — the wrapper itself does not
// expose deadlines, so SetXxxDeadline returns http.ErrNotSupported and
// our slide() swallows it. Useful for happy-path wrapper tests where
// we only care about Read/Write/Close forwarding.
type deadlineRecorder struct{}

func (deadlineRecorder) Header() http.Header       { return http.Header{} }
func (deadlineRecorder) Write([]byte) (int, error) { return 0, nil }
func (deadlineRecorder) WriteHeader(int)           {}

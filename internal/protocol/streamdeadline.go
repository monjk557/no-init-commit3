package protocol

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// StreamDeadliner replaces the http.Server fixed Read/Write deadlines for
// Smart HTTP streaming routes (git-upload-pack / git-receive-pack POST)
// with an idle-based policy.
//
// The global http.Server WriteTimeout / ReadTimeout are absolute deadlines
// measured from header read; once they fire mid-pack the connection is
// reset and the client sees `fatal: early EOF` followed by
// `fatal: fetch-pack: invalid index-pack output`. Smart HTTP fetch/push
// responses are unbounded streams whose duration is a function of repo
// size, network speed, and concurrency — they are not bounded by a
// constant.
//
// StreamDeadliner clears the fixed deadlines on the underlying connection
// and slides them forward by the idle window on every successful read or
// write. Active streams stay alive indefinitely; truly stalled streams
// are reaped after one idle window and free their concurrency slot.
//
// idle <= 0 disables the deadline entirely (uncapped). Useful for tests
// or operators who already enforce timeouts upstream.
type StreamDeadliner struct {
	rc   *http.ResponseController
	idle time.Duration

	mu sync.Mutex
}

// NewStreamDeadliner creates a deadliner bound to the response writer's
// underlying connection. The initial deadline is primed to one idle
// window; subsequent successful I/O slides it forward.
//
// If the underlying connection does not support deadlines (e.g. an
// http.ResponseWriter that does not expose Unwrap to a *net.Conn), the
// returned deadliner is a no-op — calls to Slide* succeed silently.
// In that environment the caller has no way to enforce a deadline at
// the HTTP layer regardless, so propagating an error would only mask
// the real handler logic.
func NewStreamDeadliner(w http.ResponseWriter, idle time.Duration) *StreamDeadliner {
	return &StreamDeadliner{
		rc:   http.NewResponseController(w),
		idle: idle,
	}
}

// Prime clears any fixed deadline previously set by http.Server and
// installs the initial idle window. Returns an error only when the
// connection supports deadlines but the call itself fails for another
// reason; ErrNotSupported is swallowed because it just means we cannot
// enforce a deadline here, not that anything is broken.
func (sd *StreamDeadliner) Prime() error {
	if err := sd.SlideRead(); err != nil {
		return err
	}
	return sd.SlideWrite()
}

// SlideRead advances the read deadline to now + idle. Called before each
// inbound chunk so a slow but progressing client never trips the
// deadline.
func (sd *StreamDeadliner) SlideRead() error {
	return sd.slide(sd.rc.SetReadDeadline)
}

// SlideWrite advances the write deadline to now + idle. Called before
// each outbound chunk so packfile streaming is paced by the slower of
// pack-objects and the client, not by a fixed wall clock.
func (sd *StreamDeadliner) SlideWrite() error {
	return sd.slide(sd.rc.SetWriteDeadline)
}

func (sd *StreamDeadliner) slide(set func(time.Time) error) error {
	sd.mu.Lock()
	defer sd.mu.Unlock()

	var t time.Time
	if sd.idle > 0 {
		t = time.Now().Add(sd.idle)
	}

	err := set(t)
	if err == nil {
		return nil
	}
	// Not all ResponseWriter chains expose a deadline-capable conn.
	// Treat that as best-effort: the handler can still serve the
	// request, just without our deadline policy.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// idleWriter wraps an io.Writer and slides the write deadline before
// every Write call. Use it to wrap cmd.Stdout when a child process
// streams response data into the HTTP response writer.
type idleWriter struct {
	w  io.Writer
	sd *StreamDeadliner
}

// NewIdleWriter wraps w so each Write call slides the underlying
// connection's write deadline forward by the deadliner's idle window.
func NewIdleWriter(w io.Writer, sd *StreamDeadliner) io.Writer {
	if sd == nil {
		return w
	}
	return &idleWriter{w: w, sd: sd}
}

func (iw *idleWriter) Write(p []byte) (int, error) {
	_ = iw.sd.SlideWrite()
	return iw.w.Write(p)
}

// idleReader wraps an io.Reader and slides the read deadline before
// every Read call. Use it to wrap r.Body when handing the request body
// to a child process or to a pkt-line parser.
type idleReader struct {
	r  io.Reader
	sd *StreamDeadliner
}

// NewIdleReader wraps r so each Read call slides the underlying
// connection's read deadline forward by the deadliner's idle window.
func NewIdleReader(r io.Reader, sd *StreamDeadliner) io.Reader {
	if sd == nil {
		return r
	}
	return &idleReader{r: r, sd: sd}
}

func (ir *idleReader) Read(p []byte) (int, error) {
	_ = ir.sd.SlideRead()
	return ir.r.Read(p)
}

// idleReadCloser is an io.ReadCloser variant for handlers that need to
// preserve Close semantics (e.g. wrapping http.Request.Body).
type idleReadCloser struct {
	rc io.ReadCloser
	sd *StreamDeadliner
}

// NewIdleReadCloser wraps rc so each Read call slides the underlying
// connection's read deadline. Close is forwarded unchanged.
func NewIdleReadCloser(rc io.ReadCloser, sd *StreamDeadliner) io.ReadCloser {
	if sd == nil {
		return rc
	}
	return &idleReadCloser{rc: rc, sd: sd}
}

func (irc *idleReadCloser) Read(p []byte) (int, error) {
	_ = irc.sd.SlideRead()
	return irc.rc.Read(p)
}

func (irc *idleReadCloser) Close() error {
	return irc.rc.Close()
}

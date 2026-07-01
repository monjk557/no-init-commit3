package packcache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrDisabled is returned by GetOrGenerate when the cache is off; callers should
// fall back to generating directly to the response.
var ErrDisabled = errors.New("packcache: disabled")

// Store is a disk-backed cache of generated upload-pack responses.
//
// Concurrency: identical keys coalesce via a singleflight group, so a burst of
// N identical clones triggers at most one generation; the rest wait and then
// read the freshly written file. (The generate-through and HTTP replay wiring
// lands in a follow-up commit — see doc.go.)
type Store struct {
	cfg   Config
	group singleflight.Group
}

// NewStore creates the cache directory when enabled. A disabled config yields a
// store whose Get always misses and whose Enabled() is false, so callers can
// use it unconditionally.
func NewStore(cfg Config) (*Store, error) {
	if cfg.Enabled {
		if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
			return nil, fmt.Errorf("packcache: create dir %q: %w", cfg.Dir, err)
		}
	}
	return &Store{cfg: cfg}, nil
}

// Enabled reports whether the cache is active.
func (s *Store) Enabled() bool { return s.cfg.Enabled }

// path returns the on-disk file path for a key.
func (s *Store) path(key string) string {
	return filepath.Join(s.cfg.Dir, key+".pack")
}

// Get opens a cached pack for reading if present and within TTL. The bool
// reports a hit; the caller must Close the returned file.
func (s *Store) Get(key string) (*os.File, bool, error) {
	if !s.cfg.Enabled {
		return nil, false, nil
	}
	p := s.path(key)
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if time.Since(info.ModTime()) > s.cfg.TTL {
		return nil, false, nil // stale → treat as miss
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

// GetOrGenerate serves key from cache, or generates it exactly once under a
// single-flight lock and caches the result. generate must write the full
// upload-pack response bytes to the provided writer. Concurrent callers with
// the same key share one generation; the rest wait and then read the file.
//
// Returns an open file the caller must Close, and hit=true when it was served
// from an existing cache entry (no generation ran). The HTTP replay wiring in
// HandleUploadPack is a follow-up; this method is the generation core and is
// exercised by unit tests today.
func (s *Store) GetOrGenerate(key string, generate func(io.Writer) error) (f *os.File, hit bool, err error) {
	if !s.cfg.Enabled {
		return nil, false, ErrDisabled
	}
	if cached, ok, gerr := s.Get(key); gerr != nil {
		return nil, false, gerr
	} else if ok {
		return cached, true, nil
	}

	// Miss: coalesce concurrent identical generations onto one producer.
	_, genErr, _ := s.group.Do(key, func() (any, error) {
		// Another flight may have filled the entry while we queued.
		if cached, ok, gerr := s.Get(key); gerr == nil && ok {
			_ = cached.Close()
			return nil, nil
		}
		tmp, commit, cleanup, perr := s.put(key)
		if perr != nil {
			return nil, perr
		}
		if werr := generate(tmp); werr != nil {
			cleanup()
			return nil, werr
		}
		if cerr := commit(); cerr != nil {
			cleanup()
			return nil, cerr
		}
		return nil, nil
	})
	if genErr != nil {
		return nil, false, genErr
	}

	f, _, err = s.Get(key)
	return f, false, err
}

// commitFunc finalizes a pending write.
type commitFunc func() error

// put returns a temp file to stream the generated pack into, plus a commit that
// atomically renames it into place. On any error before commit, the caller must
// call the returned cleanup to remove the temp file. Unexported until the
// generate-through path is wired.
func (s *Store) put(key string) (f *os.File, commit commitFunc, cleanup func(), err error) {
	final := s.path(key)
	tmp, err := os.CreateTemp(s.cfg.Dir, ".tmp-"+key+"-*")
	if err != nil {
		return nil, nil, nil, err
	}
	commit = func() error {
		if cerr := tmp.Close(); cerr != nil {
			return cerr
		}
		return os.Rename(tmp.Name(), final)
	}
	cleanup = func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	return tmp, commit, cleanup, nil
}

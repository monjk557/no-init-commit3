package packcache

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFromEnv_DefaultsDisabled(t *testing.T) {
	c := Default()
	if c.Enabled {
		t.Fatal("cache must default off")
	}
	if c.TTL <= 0 || c.MaxBytes <= 0 || c.Dir == "" {
		t.Fatalf("bad defaults: %+v", c)
	}
}

func TestFromEnv_Overrides(t *testing.T) {
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvDir, "/tmp/pc")
	t.Setenv(EnvTTL, "30s")
	t.Setenv(EnvMaxBytes, "1048576")

	c := FromEnv()
	if !c.Enabled || c.Dir != "/tmp/pc" || c.TTL != 30*time.Second || c.MaxBytes != 1<<20 {
		t.Fatalf("env overrides not applied: %+v", c)
	}
}

func TestFromEnv_InvalidValuesFallBack(t *testing.T) {
	t.Setenv(EnvTTL, "not-a-duration")
	t.Setenv(EnvMaxBytes, "-5")
	c := FromEnv()
	if c.TTL != Default().TTL || c.MaxBytes != Default().MaxBytes {
		t.Fatalf("invalid values should fall back to defaults: %+v", c)
	}
}

func TestDeriveKey_DeterministicAndDistinct(t *testing.T) {
	a := DeriveKey("repo1", "gen1", []byte("want abc"))
	if a != DeriveKey("repo1", "gen1", []byte("want abc")) {
		t.Fatal("key must be deterministic")
	}
	// Any input change must change the key.
	distinct := map[string]bool{a: true}
	for _, k := range []string{
		DeriveKey("repo2", "gen1", []byte("want abc")), // repo
		DeriveKey("repo1", "gen2", []byte("want abc")), // generation (push)
		DeriveKey("repo1", "gen1", []byte("want abd")), // request body
	} {
		if distinct[k] {
			t.Fatalf("expected distinct key, collided: %s", k)
		}
		distinct[k] = true
	}
}

func TestDeriveKey_NoBoundaryAliasing(t *testing.T) {
	// Length-prefixing must prevent ("ab","c") aliasing ("a","bc").
	if DeriveKey("ab", "c", nil) == DeriveKey("a", "bc", nil) {
		t.Fatal("field boundaries aliased")
	}
}

func TestStore_DisabledAlwaysMisses(t *testing.T) {
	s, err := NewStore(Default()) // disabled
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatal("should be disabled")
	}
	if _, ok, _ := s.Get("k"); ok {
		t.Fatal("disabled store must miss")
	}
	if _, _, err := s.GetOrGenerate("k", func(io.Writer) error { return nil }); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

func TestStore_GenerateThenHit(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.Dir = t.TempDir()
	s, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var gens int32
	gen := func(w io.Writer) error {
		atomic.AddInt32(&gens, 1)
		_, e := w.Write([]byte("PACKDATA"))
		return e
	}

	// First call generates.
	f, hit, err := s.GetOrGenerate("k1", gen)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("first call should be a miss/generate")
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "PACKDATA" {
		t.Fatalf("bad pack bytes: %q", b)
	}

	// Second call hits cache, no regeneration.
	f2, hit2, err := s.GetOrGenerate("k1", gen)
	if err != nil {
		t.Fatal(err)
	}
	if !hit2 {
		t.Fatal("second call should hit")
	}
	_ = f2.Close()
	if got := atomic.LoadInt32(&gens); got != 1 {
		t.Fatalf("generate ran %d times, want 1", got)
	}
}

func TestStore_SingleFlightCoalesces(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.Dir = t.TempDir()
	s, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var gens int32
	start := make(chan struct{})
	gen := func(w io.Writer) error {
		atomic.AddInt32(&gens, 1)
		<-start // hold the flight so concurrent callers pile up
		_, e := w.Write([]byte("X"))
		return e
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			f, _, gerr := s.GetOrGenerate("same", gen)
			if gerr == nil && f != nil {
				_ = f.Close()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&gens); got != 1 {
		t.Fatalf("expected 1 coalesced generation, got %d", got)
	}
}

func TestStore_TTLExpiryMisses(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.Dir = t.TempDir()
	cfg.TTL = 20 * time.Millisecond
	s, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.GetOrGenerate("k", func(w io.Writer) error {
		_, e := w.Write([]byte("d"))
		return e
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok, _ := s.Get("k"); ok {
		t.Fatal("entry should be stale after TTL")
	}
}

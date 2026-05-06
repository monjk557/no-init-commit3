package repositories

import (
	"testing"
)

func TestParseGitmodules_Empty(t *testing.T) {
	idx := ParseGitmodules(nil)
	if !idx.IsEmpty() {
		t.Fatalf("expected empty index for nil input")
	}
	if idx.LookupByPath("anything") != nil {
		t.Fatalf("expected LookupByPath to return nil on empty index")
	}
}

func TestParseGitmodules_SingleSubmodule(t *testing.T) {
	content := []byte(`[submodule "vendor/protobuf"]
	path = vendor/protobuf
	url = https://github.com/protocolbuffers/protobuf
	branch = main
`)
	idx := ParseGitmodules(content)
	cfg := idx.LookupByPath("vendor/protobuf")
	if cfg == nil {
		t.Fatalf("expected to find submodule by path")
	}
	if cfg.Name != "vendor/protobuf" {
		t.Errorf("name: got %q, want %q", cfg.Name, "vendor/protobuf")
	}
	if cfg.URL != "https://github.com/protocolbuffers/protobuf" {
		t.Errorf("url: got %q", cfg.URL)
	}
	if cfg.Branch != "main" {
		t.Errorf("branch: got %q, want %q", cfg.Branch, "main")
	}
}

func TestParseGitmodules_MultipleSubmodules(t *testing.T) {
	content := []byte(`[submodule "a"]
path = vendor/a
url = git@example.com:org/a.git

[submodule "b"]
path = vendor/b
url = https://example.com/org/b
`)
	idx := ParseGitmodules(content)
	if idx.LookupByPath("vendor/a") == nil || idx.LookupByPath("vendor/b") == nil {
		t.Fatalf("expected both submodules to be indexed")
	}
	if got := idx.LookupByPath("vendor/a").URL; got != "git@example.com:org/a.git" {
		t.Errorf("a.url: got %q", got)
	}
	if got := idx.LookupByPath("vendor/b").URL; got != "https://example.com/org/b" {
		t.Errorf("b.url: got %q", got)
	}
}

func TestParseGitmodules_IgnoresNonSubmoduleSections(t *testing.T) {
	content := []byte(`[core]
repositoryformatversion = 0

[submodule "x"]
path = vendor/x
url = https://example.com/x

[remote "origin"]
url = https://example.com/origin
`)
	idx := ParseGitmodules(content)
	if idx.LookupByPath("vendor/x") == nil {
		t.Fatalf("expected vendor/x to be indexed")
	}
	// The [core] / [remote] sections must not pollute byPath.
	if len(idx.byPath) != 1 {
		t.Errorf("expected exactly 1 entry, got %d", len(idx.byPath))
	}
}

func TestParseGitmodules_QuotedValueAndComments(t *testing.T) {
	content := []byte(`# top-level comment
[submodule "q"]
	path = "vendor/q"
	url = https://example.com/q.git ; trailing comment ignored
	branch = "release-1.x"
`)
	cfg := ParseGitmodules(content).LookupByPath("vendor/q")
	if cfg == nil {
		t.Fatalf("expected vendor/q")
	}
	if cfg.Branch != "release-1.x" {
		t.Errorf("branch: got %q", cfg.Branch)
	}
	if cfg.URL != "https://example.com/q.git" {
		t.Errorf("url should have inline comment stripped: got %q", cfg.URL)
	}
}

func TestParseGitmodules_NoPathDropsEntry(t *testing.T) {
	content := []byte(`[submodule "no-path"]
url = https://example.com/no-path
`)
	idx := ParseGitmodules(content)
	if !idx.IsEmpty() {
		t.Errorf("submodule without a path must not be indexed (cannot map to a tree entry)")
	}
}

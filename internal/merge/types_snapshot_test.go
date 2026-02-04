package merge

import (
	"sort"
	"testing"
)

func Test_NewTreeSnapshot_ReturnsEmptySnapshot(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()

	if snapshot == nil {
		t.Fatalf("NewTreeSnapshot()=nil want=non-nil")
	}
	if !snapshot.IsEmpty() {
		t.Fatalf("IsEmpty()=false want=true")
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("len(Entries)=%d want=0", len(snapshot.Entries))
	}
}

func Test_TreeSnapshot_AddEntry_AddsEntry(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()
	entry := FileSnapshot{
		Hash:  "abc123",
		Mode:  0100644,
		IsDir: false,
		Size:  100,
	}

	snapshot.AddEntry("file.txt", entry)

	if snapshot.IsEmpty() {
		t.Fatalf("IsEmpty()=true want=false after AddEntry")
	}

	got, ok := snapshot.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if got.Hash != "abc123" {
		t.Fatalf("Hash=%q want=%q", got.Hash, "abc123")
	}
}

func Test_TreeSnapshot_GetEntry_NotFound_ReturnsFalse(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()

	_, ok := snapshot.GetEntry("nonexistent.txt")

	if ok {
		t.Fatalf("GetEntry(nonexistent)=_, true want=_, false")
	}
}

func Test_TreeSnapshot_HasEntry_ReturnsCorrectly(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()
	snapshot.AddEntry("exists.txt", FileSnapshot{Hash: "abc"})

	if !snapshot.HasEntry("exists.txt") {
		t.Fatalf("HasEntry(exists.txt)=false want=true")
	}
	if snapshot.HasEntry("nonexistent.txt") {
		t.Fatalf("HasEntry(nonexistent.txt)=true want=false")
	}
}

func Test_TreeSnapshot_Paths_ReturnsAllPaths(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()
	snapshot.AddEntry("a.txt", FileSnapshot{Hash: "a"})
	snapshot.AddEntry("b.txt", FileSnapshot{Hash: "b"})
	snapshot.AddEntry("c/d.txt", FileSnapshot{Hash: "d"})

	paths := snapshot.Paths()

	if len(paths) != 3 {
		t.Fatalf("len(Paths())=%d want=3", len(paths))
	}

	// Sort for deterministic comparison
	sort.Strings(paths)
	expected := []string{"a.txt", "b.txt", "c/d.txt"}

	for i, want := range expected {
		if paths[i] != want {
			t.Fatalf("paths[%d]=%q want=%q", i, paths[i], want)
		}
	}
}

func Test_TreeSnapshot_IsEmpty_ReturnsCorrectly(t *testing.T) {
	t.Parallel()

	snapshot := NewTreeSnapshot()

	if !snapshot.IsEmpty() {
		t.Fatalf("IsEmpty()=false want=true for new snapshot")
	}

	snapshot.AddEntry("file.txt", FileSnapshot{Hash: "abc"})

	if snapshot.IsEmpty() {
		t.Fatalf("IsEmpty()=true want=false after adding entry")
	}
}

func Test_FileSnapshot_StoresAllFields(t *testing.T) {
	t.Parallel()

	fs := FileSnapshot{
		Hash:  "abc123def456",
		Mode:  0100644,
		IsDir: false,
		Size:  1024,
	}

	if fs.Hash != "abc123def456" {
		t.Fatalf("Hash=%q want=%q", fs.Hash, "abc123def456")
	}
	if fs.Mode != 0100644 {
		t.Fatalf("Mode=%o want=%o", fs.Mode, 0100644)
	}
	if fs.IsDir != false {
		t.Fatalf("IsDir=%v want=false", fs.IsDir)
	}
	if fs.Size != 1024 {
		t.Fatalf("Size=%d want=1024", fs.Size)
	}
}

func Test_FileSnapshot_Directory_HasCorrectFields(t *testing.T) {
	t.Parallel()

	fs := FileSnapshot{
		Hash:  "tree123",
		Mode:  0040000, // directory mode
		IsDir: true,
		Size:  0,
	}

	if !fs.IsDir {
		t.Fatalf("IsDir=false want=true for directory")
	}
	if fs.Mode != 0040000 {
		t.Fatalf("Mode=%o want=%o for directory", fs.Mode, 0040000)
	}
}

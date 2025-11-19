package git

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/collabops/git-executor/internal/git"
)

// TestSortTreeEntries tests that tree entries are sorted correctly:
// 1. Folders (Tree) come before files (Blob)
// 2. Files come before submodules (Commit)
// 3. Within each group, entries are sorted alphabetically (ASCII order)
func TestSortTreeEntries(t *testing.T) {
	t.Run("폴더가_파일보다_먼저", func(t *testing.T) {
		entries := []git.TreeEntry{
			{Name: "file.txt", Type: git.EntryTypeBlob},
			{Name: "folder", Type: git.EntryTypeTree},
			{Name: "another-file.txt", Type: git.EntryTypeBlob},
			{Name: "another-folder", Type: git.EntryTypeTree},
		}

		git.SortTreeEntries(entries)

		// Find first file index
		firstFileIndex := -1
		for i, entry := range entries {
			if entry.Type == git.EntryTypeBlob {
				firstFileIndex = i
				break
			}
		}

		// Find last folder index
		lastFolderIndex := -1
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Type == git.EntryTypeTree {
				lastFolderIndex = i
				break
			}
		}

		// All folders should come before all files
		assert.Less(t, lastFolderIndex, firstFileIndex,
			"All folders should come before all files. Last folder at %d, first file at %d",
			lastFolderIndex, firstFileIndex)
	})

	t.Run("폴더_그룹_내_사전순_정렬", func(t *testing.T) {
		entries := []git.TreeEntry{
			{Name: "zzz-folder", Type: git.EntryTypeTree},
			{Name: "aaa-folder", Type: git.EntryTypeTree},
			{Name: "mmm-folder", Type: git.EntryTypeTree},
		}

		git.SortTreeEntries(entries)

		// Verify folders are sorted alphabetically
		assert.Equal(t, "aaa-folder", entries[0].Name)
		assert.Equal(t, "mmm-folder", entries[1].Name)
		assert.Equal(t, "zzz-folder", entries[2].Name)
	})

	t.Run("파일_그룹_내_사전순_정렬", func(t *testing.T) {
		entries := []git.TreeEntry{
			{Name: "zzz-file.txt", Type: git.EntryTypeBlob},
			{Name: "aaa-file.txt", Type: git.EntryTypeBlob},
			{Name: "README.md", Type: git.EntryTypeBlob},
			{Name: "bbb-file.txt", Type: git.EntryTypeBlob},
		}

		git.SortTreeEntries(entries)

		// Verify files are sorted alphabetically (ASCII order)
		// 'R' (82) < 'a' (97) in ASCII, so README.md comes before aaa-file.txt
		assert.Equal(t, "README.md", entries[0].Name)
		assert.Equal(t, "aaa-file.txt", entries[1].Name)
		assert.Equal(t, "bbb-file.txt", entries[2].Name)
		assert.Equal(t, "zzz-file.txt", entries[3].Name)
	})

	t.Run("전체_정렬_순서_검증", func(t *testing.T) {
		// Create entries in intentionally unsorted order
		entries := []git.TreeEntry{
			{Name: "zzz-file.txt", Type: git.EntryTypeBlob},
			{Name: "aaa-file.txt", Type: git.EntryTypeBlob},
			{Name: "README.md", Type: git.EntryTypeBlob},
			{Name: "zzz-folder", Type: git.EntryTypeTree},
			{Name: "aaa-folder", Type: git.EntryTypeTree},
			{Name: "bbb-file.txt", Type: git.EntryTypeBlob},
		}

		git.SortTreeEntries(entries)

		// Expected order:
		// 1. aaa-folder (Tree)
		// 2. zzz-folder (Tree)
		// 3. README.md (Blob) - 'R' < 'a' in ASCII
		// 4. aaa-file.txt (Blob)
		// 5. bbb-file.txt (Blob)
		// 6. zzz-file.txt (Blob)

		assert.Equal(t, "aaa-folder", entries[0].Name)
		assert.Equal(t, git.EntryTypeTree, entries[0].Type)

		assert.Equal(t, "zzz-folder", entries[1].Name)
		assert.Equal(t, git.EntryTypeTree, entries[1].Type)

		assert.Equal(t, "README.md", entries[2].Name)
		assert.Equal(t, git.EntryTypeBlob, entries[2].Type)

		assert.Equal(t, "aaa-file.txt", entries[3].Name)
		assert.Equal(t, git.EntryTypeBlob, entries[3].Type)

		assert.Equal(t, "bbb-file.txt", entries[4].Name)
		assert.Equal(t, git.EntryTypeBlob, entries[4].Type)

		assert.Equal(t, "zzz-file.txt", entries[5].Name)
		assert.Equal(t, git.EntryTypeBlob, entries[5].Type)
	})

	t.Run("서브모듈이_마지막에", func(t *testing.T) {
		entries := []git.TreeEntry{
			{Name: "file.txt", Type: git.EntryTypeBlob},
			{Name: "submodule", Type: git.EntryTypeCommit},
			{Name: "folder", Type: git.EntryTypeTree},
		}

		git.SortTreeEntries(entries)

		// Expected order: folder, file.txt, submodule
		assert.Equal(t, "folder", entries[0].Name)
		assert.Equal(t, git.EntryTypeTree, entries[0].Type)

		assert.Equal(t, "file.txt", entries[1].Name)
		assert.Equal(t, git.EntryTypeBlob, entries[1].Type)

		assert.Equal(t, "submodule", entries[2].Name)
		assert.Equal(t, git.EntryTypeCommit, entries[2].Type)
	})

	t.Run("같은_타입_내_사전순_정렬_대소문자", func(t *testing.T) {
		// ASCII order: 'A' (65) < 'a' (97)
		entries := []git.TreeEntry{
			{Name: "apple.txt", Type: git.EntryTypeBlob},
			{Name: "Apple.txt", Type: git.EntryTypeBlob},
			{Name: "banana.txt", Type: git.EntryTypeBlob},
		}

		git.SortTreeEntries(entries)

		// 'A' < 'a' < 'b' in ASCII
		assert.Equal(t, "Apple.txt", entries[0].Name)
		assert.Equal(t, "apple.txt", entries[1].Name)
		assert.Equal(t, "banana.txt", entries[2].Name)
	})

	t.Run("빈_슬라이스", func(t *testing.T) {
		entries := []git.TreeEntry{}
		git.SortTreeEntries(entries)
		assert.Equal(t, 0, len(entries))
	})

	t.Run("단일_항목", func(t *testing.T) {
		entries := []git.TreeEntry{
			{Name: "file.txt", Type: git.EntryTypeBlob},
		}
		git.SortTreeEntries(entries)
		assert.Equal(t, 1, len(entries))
		assert.Equal(t, "file.txt", entries[0].Name)
	})
}

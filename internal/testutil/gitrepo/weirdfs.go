package gitrepo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Weird Filesystem Cases (R4_weirdfs)
// 회귀: "대소문자/유니코드/심볼릭링크가 섞여도 path/branch가 충돌하거나 탈출하지 않는다"
// =============================================================================

// CaseCollisionResult contains case collision test data
type CaseCollisionResult struct {
	LowerCaseBranch string
	UpperCaseBranch string
	LowerCaseCommit plumbing.Hash
	UpperCaseCommit plumbing.Hash
}

// MakeCaseCollision creates branches that would collide on case-insensitive FS
// 회귀: "mac 기본 FS에서 Feature vs feature 충돌이 적절히 처리된다"
func (r *TestRepo) MakeCaseCollision() CaseCollisionResult {
	r.T.Helper()

	// Base commit
	r.Commit("base.txt", "base", "Base commit")

	// Create lowercase branch
	r.BranchCreate("feature")
	lowerCommit := r.Commit("lower.txt", "lower", "Lower case commit")

	// Create uppercase branch (may fail on case-insensitive FS)
	r.BranchCheckout("main")
	r.BranchCreate("Feature")
	upperCommit := r.Commit("upper.txt", "upper", "Upper case commit")

	r.BranchCheckout("main")

	return CaseCollisionResult{
		LowerCaseBranch: "feature",
		UpperCaseBranch: "Feature",
		LowerCaseCommit: lowerCommit,
		UpperCaseCommit: upperCommit,
	}
}

// IsCaseInsensitiveFS checks if the filesystem is case-insensitive
func IsCaseInsensitiveFS() bool {
	// macOS is typically case-insensitive
	return runtime.GOOS == "darwin"
}

// SkipOnCaseSensitiveFS skips test if FS is case-sensitive
func SkipOnCaseSensitiveFS(t interface{ Skip(args ...interface{}) }) {
	if !IsCaseInsensitiveFS() {
		t.Skip("skipping case collision test on case-sensitive filesystem")
	}
}

// SkipOnCaseInsensitiveFS skips test if FS is case-insensitive
func SkipOnCaseInsensitiveFS(t interface{ Skip(args ...interface{}) }) {
	if IsCaseInsensitiveFS() {
		t.Skip("skipping test on case-insensitive filesystem")
	}
}

// =============================================================================
// Unicode Normalization Cases
// 회귀: "유니코드 정규화 차이로 경로가 충돌하지 않는다"
// =============================================================================

// UnicodeNormalizationResult contains unicode test data
type UnicodeNormalizationResult struct {
	NFCPath    string // NFC normalized path
	NFDPath    string // NFD normalized path
	NFCCommit  plumbing.Hash
	NFDCommit  plumbing.Hash
	TestString string // The unicode string being tested
}

// MakeUnicodeNormalization creates files with different unicode normalizations
// NFC: é = U+00E9 (single codepoint)
// NFD: é = U+0065 + U+0301 (base + combining)
func (r *TestRepo) MakeUnicodeNormalization() UnicodeNormalizationResult {
	r.T.Helper()

	// Base commit
	r.Commit("base.txt", "base", "Base commit")

	// NFC: é as single codepoint
	nfcString := "caf\u00E9.txt"
	nfcCommit := r.Commit(nfcString, "NFC content", "NFC commit")

	// NFD: é as base + combining mark (may collide on macOS)
	r.BranchCreate("unicode-test")
	nfdString := "cafe\u0301.txt"
	nfdCommit := r.Commit(nfdString, "NFD content", "NFD commit")

	r.BranchCheckout("main")

	return UnicodeNormalizationResult{
		NFCPath:    nfcString,
		NFDPath:    nfdString,
		NFCCommit:  nfcCommit,
		NFDCommit:  nfdCommit,
		TestString: "café",
	}
}

// =============================================================================
// Symlink Cases
// 회귀: "심볼릭 링크가 repo root 밖을 가리켜도 보안 문제 없이 처리된다"
// =============================================================================

// SymlinkResult contains symlink test data
type SymlinkResult struct {
	SafeSymlink   string // Symlink within repo
	UnsafeSymlink string // Symlink pointing outside repo
	SafeTarget    string
	UnsafeTarget  string
	CommitHash    plumbing.Hash
}

// MakeSymlinks creates symlink scenarios
func (r *TestRepo) MakeSymlinks() SymlinkResult {
	r.T.Helper()

	// Skip on Windows (no symlink support in most cases)
	if runtime.GOOS == "windows" {
		r.T.Skip("symlink tests not supported on Windows")
	}

	// Create a target file
	r.Commit("target.txt", "target content", "Target commit")

	// Create safe symlink (within repo)
	safeSymlink := filepath.Join(r.Dir, "safe-link.txt")
	err := os.Symlink("target.txt", safeSymlink)
	require.NoError(r.T, err)
	r.StageFile("safe-link.txt")

	// Create unsafe symlink (pointing outside repo)
	unsafeSymlink := filepath.Join(r.Dir, "unsafe-link.txt")
	err = os.Symlink("/etc/passwd", unsafeSymlink)
	require.NoError(r.T, err)
	r.StageFile("unsafe-link.txt")

	hash := r.CommitStaged("Add symlinks")

	return SymlinkResult{
		SafeSymlink:   "safe-link.txt",
		UnsafeSymlink: "unsafe-link.txt",
		SafeTarget:    "target.txt",
		UnsafeTarget:  "/etc/passwd",
		CommitHash:    hash,
	}
}

// MakeSymlinkTraversal creates symlink that attempts path traversal
func (r *TestRepo) MakeSymlinkTraversal() (symlinkPath string, commitHash plumbing.Hash) {
	r.T.Helper()

	if runtime.GOOS == "windows" {
		r.T.Skip("symlink tests not supported on Windows")
	}

	// Create base
	r.Commit("base.txt", "base", "Base commit")

	// Create symlink that traverses up
	symlinkPath = filepath.Join(r.Dir, "traversal-link")
	err := os.Symlink("../../../etc/passwd", symlinkPath)
	require.NoError(r.T, err)
	r.StageFile("traversal-link")

	commitHash = r.CommitStaged("Add traversal symlink")
	return "traversal-link", commitHash
}

// =============================================================================
// Executable Bit Cases
// 회귀: "executable bit 변경이 정확히 감지된다"
// =============================================================================

// MakeExecutableBitChange creates scenario with filemode change
func (r *TestRepo) MakeExecutableBitChange() (source, target plumbing.Hash) {
	r.T.Helper()

	if runtime.GOOS == "windows" {
		r.T.Skip("executable bit tests not reliable on Windows")
	}

	// Create non-executable file
	filePath := filepath.Join(r.Dir, "script.sh")
	err := os.WriteFile(filePath, []byte("#!/bin/bash\necho hello"), 0644)
	require.NoError(r.T, err)
	r.StageFile("script.sh")
	target = r.CommitStaged("Add non-executable script")

	// Make it executable in feature branch
	r.BranchCreate("make-executable")
	err = os.Chmod(filePath, 0755)
	require.NoError(r.T, err)
	r.StageFile("script.sh")
	source = r.CommitStaged("Make script executable")

	r.BranchCheckout("main")
	return source, target
}

// =============================================================================
// Special Characters in Paths
// 회귀: "특수문자가 포함된 경로도 안전하게 처리된다"
// =============================================================================

// SpecialCharPathResult contains special character path test data
type SpecialCharPathResult struct {
	Paths      []string
	CommitHash plumbing.Hash
}

// MakeSpecialCharPaths creates files with special characters in names
// Note: Some characters may not work on all filesystems
func (r *TestRepo) MakeSpecialCharPaths() SpecialCharPathResult {
	r.T.Helper()

	// Safe special characters (work on most filesystems)
	paths := []string{
		"file-with-dash.txt",
		"file_with_underscore.txt",
		"file.with.dots.txt",
		"file with spaces.txt",
		"file@at.txt",
		"file#hash.txt",
		"file%percent.txt",
		"file+plus.txt",
		"file=equals.txt",
	}

	files := make(map[string]string)
	for _, p := range paths {
		files[p] = "content for " + p
	}

	hash := r.CommitMultiple(files, "Add special char files")

	return SpecialCharPathResult{
		Paths:      paths,
		CommitHash: hash,
	}
}

// =============================================================================
// Git-forbidden Characters
// 회귀: "Git이 금지하는 문자가 올바르게 거부된다"
// =============================================================================

// ForbiddenBranchNames returns list of invalid branch names
func ForbiddenBranchNames() []string {
	return []string{
		"",                       // Empty
		".",                      // Dot only
		"..",                     // Double dot
		".lock",                  // Starts with dot
		"branch.lock",            // Ends with .lock
		"branch..name",           // Contains ..
		"branch~1",               // Contains ~
		"branch^",                // Contains ^
		"branch:",                // Contains :
		"branch\\path",           // Contains backslash
		"branch?name",            // Contains ?
		"branch*name",            // Contains *
		"branch[name",            // Contains [
		"branch@{1}",             // Contains @{
		"/branch",                // Starts with /
		"branch/",                // Ends with /
		"branch//name",           // Contains //
		"branch\x00name",         // Contains null
		"branch\nname",           // Contains newline
		"branch\tname",           // Contains tab
		strings.Repeat("a", 256), // Too long
	}
}

// ValidBranchNames returns list of valid but unusual branch names
func ValidBranchNames() []string {
	return []string{
		"feature/foo",
		"feature/foo/bar",
		"release-1.0.0",
		"hotfix_123",
		"UPPERCASE",
		"MixedCase",
		"123numeric",
		"abc123def",
		"branch-with-many-dashes",
		"branch.with.dots",
	}
}

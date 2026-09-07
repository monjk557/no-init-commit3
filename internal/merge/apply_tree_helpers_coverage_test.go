package merge

import (
	"testing"

	"github.com/collabops/git-executor/internal/diffs"
	"github.com/stretchr/testify/require"
)

// 회귀: "apply_tree.go 의 path/glob 헬퍼들은 환경 파일 보호 분기를 지탱하므로
// 회귀 보호가 필요하다." 모두 순수 함수 — fixture 불필요.

func Test_splitPath(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"a", "b", "c"}, splitPath("a/b/c"))
	require.Equal(t, []string{"a"}, splitPath("a"))
	require.Equal(t, []string{"a", "b"}, splitPath("/a//b/")) // empty segments dropped
	require.Nil(t, splitPath(""))
	require.Nil(t, splitPath("///"))
}

func Test_parentDir(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a/b", parentDir("a/b/c.txt"))
	require.Equal(t, "a", parentDir("a/b"))
	require.Equal(t, "", parentDir("single"))
	require.Equal(t, "", parentDir(""))
}

func Test_baseName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "c.txt", baseName("a/b/c.txt"))
	require.Equal(t, "single", baseName("single"))
	require.Equal(t, "", baseName(""))
}

func Test_matchPath(t *testing.T) {
	t.Parallel()

	// exact
	require.True(t, matchPath(".env", ".env"))
	// leading wildcard suffix match (*.env)
	require.True(t, matchPath("prod.env", "*.env"))
	require.False(t, matchPath("env.txt", "*.env"))
	// trailing wildcard prefix match (config/secrets*)
	require.True(t, matchPath("config/secrets.yml", "config/secrets*"))
	require.False(t, matchPath("config/other.yml", "config/secrets*"))
	// .env.* special case
	require.True(t, matchPath(".env.local", ".env.*"))
	require.False(t, matchPath(".envrc", ".env.*"))
	// no match
	require.False(t, matchPath("README.md", ".env"))
}

func Test_isEnvironmentFile(t *testing.T) {
	t.Parallel()

	require.True(t, isEnvironmentFile(".env"))
	require.True(t, isEnvironmentFile(".env.local"))
	require.True(t, isEnvironmentFile("service.env"))
	require.True(t, isEnvironmentFile("config/secrets.yaml"))
	require.True(t, isEnvironmentFile("config/credentials.json"))
	require.False(t, isEnvironmentFile("main.go"))
	require.False(t, isEnvironmentFile("README.md"))
}

func Test_parseModeToUint32(t *testing.T) {
	t.Parallel()

	require.Equal(t, uint32(0o100644), parseModeToUint32("100644"))
	require.Equal(t, uint32(0o040000), parseModeToUint32("040000"))
	require.Equal(t, uint32(0o120000), parseModeToUint32("120000"))
	require.Equal(t, uint32(0), parseModeToUint32(""))
	require.Equal(t, uint32(0o100644), parseModeToUint32("  100644  ")) // trimmed
}

// ---------------------------------------------------------------------------
// preview.go pure helpers
// ---------------------------------------------------------------------------

func Test_diffEntryToStatus(t *testing.T) {
	t.Parallel()

	require.Equal(t, "added", string(diffEntryToStatus("A")))
	require.Equal(t, "deleted", string(diffEntryToStatus("D")))
	require.Equal(t, "renamed", string(diffEntryToStatus("R")))
	require.Equal(t, "renamed", string(diffEntryToStatus("C")))
	require.Equal(t, "modified", string(diffEntryToStatus("M")))
	require.Equal(t, "modified", string(diffEntryToStatus("T")))
	require.Equal(t, "modified", string(diffEntryToStatus("Z"))) // unknown → modified
}

func Test_detectFileType(t *testing.T) {
	t.Parallel()

	require.Equal(t, "go", detectFileType("main.go"))
	require.Equal(t, "javascript", detectFileType("app.ts"))
	require.Equal(t, "javascript", detectFileType("app.js"))
	require.Equal(t, "python", detectFileType("script.py"))
	require.Equal(t, "java", detectFileType("Main.java"))
	require.Equal(t, "cpp", detectFileType("util.cpp"))
	require.Equal(t, "markdown", detectFileType("README.md"))
	require.Equal(t, "json", detectFileType("data.json"))
	require.Equal(t, "yaml", detectFileType("conf.yml"))
	require.Equal(t, "unknown", detectFileType("noext"))
}

func Test_isCodeFile(t *testing.T) {
	t.Parallel()

	require.True(t, isCodeFile("main.go"))
	require.True(t, isCodeFile("lib.rs"))
	require.True(t, isCodeFile("Foo.cs"))
	require.False(t, isCodeFile("README.md"))
	require.False(t, isCodeFile("data.json"))
}

func Test_analyzeComplexityChange(t *testing.T) {
	t.Parallel()

	require.Equal(t, "high_complexity_increase", analyzeComplexityChange(&FileChange{FileDiff: diffs.FileDiff{Additions: 150}}))
	require.Equal(t, "moderate_complexity_increase", analyzeComplexityChange(&FileChange{FileDiff: diffs.FileDiff{Additions: 30}}))
	require.Equal(t, "complexity_decrease", analyzeComplexityChange(&FileChange{FileDiff: diffs.FileDiff{Deletions: 60}}))
	require.Equal(t, "low_complexity_change", analyzeComplexityChange(&FileChange{FileDiff: diffs.FileDiff{Additions: 5, Deletions: 2}}))
}

// ---------------------------------------------------------------------------
// conflict_detector.go pure helpers
// ---------------------------------------------------------------------------

func Test_buildFileVersionFromDiff(t *testing.T) {
	t.Parallel()

	// nil diff → not exists
	require.False(t, buildFileVersionFromDiff(nil, true).Exists)

	// deleted → not exists
	require.False(t, buildFileVersionFromDiff(&DiffEntry{Status: "D"}, true).Exists)

	// modified → exists with hash/mode parsed
	fv := buildFileVersionFromDiff(&DiffEntry{Status: "M", DstHash: "abc", DstMode: "100644"}, false)
	require.True(t, fv.Exists)
	require.Equal(t, "abc", fv.Hash)
	require.Equal(t, uint32(0o100644), fv.Mode)
}

func Test_buildBaseFileVersionFromDiff(t *testing.T) {
	t.Parallel()

	// both nil → not exists
	require.False(t, buildBaseFileVersionFromDiff(nil, nil).Exists)

	// added (status A) → base did not exist
	require.False(t, buildBaseFileVersionFromDiff(&DiffEntry{Status: "A"}, nil).Exists)

	// modified → base exists from SrcHash/SrcMode
	fv := buildBaseFileVersionFromDiff(&DiffEntry{Status: "M", SrcHash: "base", SrcMode: "100644"}, nil)
	require.True(t, fv.Exists)
	require.Equal(t, "base", fv.Hash)
	require.Equal(t, uint32(0o100644), fv.Mode)

	// nil source but target present → fall back to target
	fv = buildBaseFileVersionFromDiff(nil, &DiffEntry{Status: "M", SrcHash: "tbase", SrcMode: "100755"})
	require.True(t, fv.Exists)
	require.Equal(t, "tbase", fv.Hash)
}

func Test_determineConflictType(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}

	// delete vs modify (source deleted): baseExists, !sourceExists, targetExists
	require.Equal(t, ConflictTypeDelete, cd.determineConflictType(true, false, true, nil, nil))
	// delete vs modify (target deleted)
	require.Equal(t, ConflictTypeDelete, cd.determineConflictType(true, true, false, nil, nil))
	// add/add: !baseExists, sourceExists, targetExists
	require.Equal(t, ConflictTypeAdd, cd.determineConflictType(false, true, true, nil, nil))
	// mode conflict: same dst hash, different dst mode
	src := &DiffEntry{DstHash: "same", DstMode: "100644"}
	tgt := &DiffEntry{DstHash: "same", DstMode: "100755"}
	require.Equal(t, ConflictTypeMode, cd.determineConflictType(true, true, true, src, tgt))
	// default content
	require.Equal(t, ConflictTypeContent, cd.determineConflictType(true, true, true, nil, nil))
}

func Test_calculateConflictSeverity(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}

	require.Equal(t, ConflictSeverityLow, cd.calculateConflictSeverity(ConflictTypeMode, nil, nil))
	require.Equal(t, ConflictSeverityMedium, cd.calculateConflictSeverity(ConflictTypeAdd, nil, nil))
	require.Equal(t, ConflictSeverityHigh, cd.calculateConflictSeverity(ConflictTypeAdd,
		&FileVersion{IsBinary: true}, &FileVersion{IsBinary: false}))
	require.Equal(t, ConflictSeverityHigh, cd.calculateConflictSeverity(ConflictTypeDelete, nil, nil))
	require.Equal(t, ConflictSeverityMedium, cd.calculateConflictSeverity(ConflictTypeContent, nil, nil))
	require.Equal(t, ConflictSeverityCritical, cd.calculateConflictSeverity(ConflictTypeContent,
		&FileVersion{IsBinary: true}, nil))
	require.Equal(t, ConflictSeverityMedium, cd.calculateConflictSeverity(ConflictTypeSymlink, nil, nil))
}

func Test_calculateSeverityBreakdown(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}
	conflicts := []FileConflict{
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityHigh},
	}
	bd := cd.calculateSeverityBreakdown(conflicts)
	require.Equal(t, 2, bd[ConflictSeverityLow])
	require.Equal(t, 1, bd[ConflictSeverityHigh])
	require.Equal(t, 0, bd[ConflictSeverityMedium])
}

func Test_estimateResolutionTime(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}
	conflicts := []FileConflict{
		{Severity: ConflictSeverityLow},      // 2
		{Severity: ConflictSeverityMedium},   // 10
		{Severity: ConflictSeverityHigh},     // 30
		{Severity: ConflictSeverityCritical}, // 60
	}
	d := cd.estimateResolutionTime(conflicts)
	require.Equal(t, 102, int(d.Minutes()))
	require.Zero(t, cd.estimateResolutionTime(nil))
}

func Test_generateRecommendations(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}

	require.Contains(t, cd.generateRecommendations(nil, MergeStrategyRecursive)[0], "No conflicts detected")

	conflicts := []FileConflict{{}, {}}
	recsRec := cd.generateRecommendations(conflicts, MergeStrategyRecursive)
	require.Contains(t, recsRec[0], "2 conflicts require manual resolution")
	require.Contains(t, recsRec[1], "Recursive strategy")

	require.Contains(t, cd.generateRecommendations(conflicts, MergeStrategyOurs)[1], "'ours' strategy")
	require.Contains(t, cd.generateRecommendations(conflicts, MergeStrategyTheirs)[1], "'theirs' strategy")
}

package merge

import (
	"testing"
)

func TestIsGitlinkMode(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"160000":  true,
		"0160000": true, // zero-padded
		"100644":  false,
		"040000":  false,
		"":        false,
		"160001":  false,
	}
	for in, want := range cases {
		got := isGitlinkMode(in)
		if got != want {
			t.Errorf("isGitlinkMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsSubmoduleConflict(t *testing.T) {
	t.Parallel()

	mkDiff := func(srcMode, srcHash, dstMode, dstHash string) *DiffEntry {
		return &DiffEntry{SrcMode: srcMode, SrcHash: srcHash, DstMode: dstMode, DstHash: dstHash}
	}

	tests := []struct {
		name       string
		source     *DiffEntry
		target     *DiffEntry
		wantSubmod bool
	}{
		{
			name:       "both gitlink with different hashes → submodule conflict",
			source:     mkDiff("160000", "aaa", "160000", "bbb"),
			target:     mkDiff("160000", "aaa", "160000", "ccc"),
			wantSubmod: true,
		},
		{
			name:       "both gitlink but identical hashes → not a conflict (= no real diff)",
			source:     mkDiff("160000", "aaa", "160000", "bbb"),
			target:     mkDiff("160000", "aaa", "160000", "bbb"),
			wantSubmod: false,
		},
		{
			name:       "only one side gitlink → fall through to other types",
			source:     mkDiff("160000", "aaa", "160000", "bbb"),
			target:     mkDiff("100644", "aaa", "100644", "ccc"),
			wantSubmod: false,
		},
		{
			name:       "neither side gitlink → not submodule",
			source:     mkDiff("100644", "aaa", "100644", "bbb"),
			target:     mkDiff("100644", "aaa", "100644", "ccc"),
			wantSubmod: false,
		},
		{
			name:       "nil source → false",
			source:     nil,
			target:     mkDiff("160000", "aaa", "160000", "bbb"),
			wantSubmod: false,
		},
		{
			name:       "nil target → false",
			source:     mkDiff("160000", "aaa", "160000", "bbb"),
			target:     nil,
			wantSubmod: false,
		},
		{
			name:       "zero-padded mode is recognized",
			source:     mkDiff("0160000", "aaa", "0160000", "bbb"),
			target:     mkDiff("0160000", "aaa", "0160000", "ccc"),
			wantSubmod: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isSubmoduleConflict(tc.source, tc.target)
			if got != tc.wantSubmod {
				t.Errorf("got %v, want %v", got, tc.wantSubmod)
			}
		})
	}
}

func TestNonZeroBaseHash(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source *DiffEntry
		target *DiffEntry
		want   string
	}{
		{
			name:   "both have base hash → return source's",
			source: &DiffEntry{SrcHash: "aaa"},
			target: &DiffEntry{SrcHash: "aaa"},
			want:   "aaa",
		},
		{
			name:   "source has zero, target has hash → return target's",
			source: &DiffEntry{SrcHash: "0000000000000000000000000000000000000000"},
			target: &DiffEntry{SrcHash: "bbb"},
			want:   "bbb",
		},
		{
			name:   "both zero → empty",
			source: &DiffEntry{SrcHash: "0000000000000000000000000000000000000000"},
			target: &DiffEntry{SrcHash: "0000000000000000000000000000000000000000"},
			want:   "",
		},
		{
			name:   "both nil → empty",
			source: nil,
			target: nil,
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nonZeroBaseHash(tc.source, tc.target)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// determineConflictType 가 양 브랜치 gitlink 변경 시 ConflictTypeSubmodule 을
// 우선 분기하는지 검증. content / mode / add / delete 분기보다 먼저 평가되어야 함.
func TestDetermineConflictType_SubmodulePrecedence(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}
	source := &DiffEntry{SrcMode: "160000", SrcHash: "aaa", DstMode: "160000", DstHash: "bbb"}
	target := &DiffEntry{SrcMode: "160000", SrcHash: "aaa", DstMode: "160000", DstHash: "ccc"}

	got := cd.determineConflictType(true, true, true, source, target)
	if got != ConflictTypeSubmodule {
		t.Errorf("got %q, want %q", got, ConflictTypeSubmodule)
	}
}

// 같은 파라미터로 한 쪽만 gitlink 면 일반 분기 (content) 로 떨어져야 함.
func TestDetermineConflictType_OnlyOneGitlink_FallsThroughToContent(t *testing.T) {
	t.Parallel()

	cd := &ConflictDetector{}
	source := &DiffEntry{SrcMode: "160000", SrcHash: "aaa", DstMode: "160000", DstHash: "bbb"}
	target := &DiffEntry{SrcMode: "100644", SrcHash: "aaa", DstMode: "100644", DstHash: "ccc"}

	got := cd.determineConflictType(true, true, true, source, target)
	if got == ConflictTypeSubmodule {
		t.Errorf("must NOT classify as submodule when only one side is gitlink, got %q", got)
	}
}

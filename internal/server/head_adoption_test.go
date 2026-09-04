package server

import (
	"sort"
	"testing"
)

func TestCreatedBranchRefs(t *testing.T) {
	cases := []struct {
		name   string
		before map[string]string
		after  map[string]string
		want   []string
	}{
		{
			name:   "첫 push — 브랜치 하나 생성",
			before: map[string]string{},
			after:  map[string]string{"refs/heads/develop": "aaa"},
			want:   []string{"refs/heads/develop"},
		},
		{
			name:   "기존 브랜치 갱신은 후보가 아니다",
			before: map[string]string{"refs/heads/main": "aaa"},
			after:  map[string]string{"refs/heads/main": "bbb"},
			want:   []string{},
		},
		{
			name:   "태그 생성은 후보가 아니다",
			before: map[string]string{},
			after:  map[string]string{"refs/tags/v1": "aaa"},
			want:   []string{},
		},
		{
			name:   "갱신과 생성이 섞이면 생성만",
			before: map[string]string{"refs/heads/main": "aaa"},
			after: map[string]string{
				"refs/heads/main":    "bbb",
				"refs/heads/feature": "ccc",
				"refs/tags/v1":       "ddd",
			},
			want: []string{"refs/heads/feature"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := createdBranchRefs(tc.before, tc.after)
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("createdBranchRefs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("createdBranchRefs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

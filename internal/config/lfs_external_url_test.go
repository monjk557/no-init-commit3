package config

import "testing"

func TestNormalizeLFSExternalURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "host base gets /api/v2 appended",
			in:   "https://api.stage.collabops.ai",
			want: "https://api.stage.collabops.ai/api/v2",
		},
		{
			name: "trailing slash trimmed then appended",
			in:   "https://api.stage.collabops.ai/",
			want: "https://api.stage.collabops.ai/api/v2",
		},
		{
			name: "already full base with /api/v2 left as-is",
			in:   "https://api.collabops.ai/api/v2",
			want: "https://api.collabops.ai/api/v2",
		},
		{
			name: "future /api/v3 not clobbered",
			in:   "https://api.collabops.ai/api/v3",
			want: "https://api.collabops.ai/api/v3",
		},
		{
			name: "local host:port base",
			in:   "http://localhost:2345",
			want: "http://localhost:2345/api/v2",
		},
		{
			name: "whitespace trimmed",
			in:   "  https://api.stage.collabops.ai  ",
			want: "https://api.stage.collabops.ai/api/v2",
		},
		{
			name: "empty stays empty (SSH LFS disabled)",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeLFSExternalURL(tt.in); got != tt.want {
				t.Fatalf("normalizeLFSExternalURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

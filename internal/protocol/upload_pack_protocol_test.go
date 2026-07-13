package protocol

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

// pkt builds a git pkt-line (4-hex length prefix + payload) for test bodies.
func pkt(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}

func TestIsProtocolV2Body(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "v2 fetch command",
			body: pkt("command=fetch\n") + pkt("agent=git/2.43\n") + "0000",
			want: true,
		},
		{
			name: "v2 ls-refs command",
			body: pkt("command=ls-refs\n") + "0000",
			want: true,
		},
		{
			name: "v0 want line",
			body: pkt("want 74730d410fcb6603ace96f1dc55ea6196122532d multi_ack_detailed side-band-64k\n") + "00000009done\n",
			want: false,
		},
		{
			name: "empty body",
			body: "",
			want: false,
		},
		{
			name: "flush only",
			body: "0000",
			want: false,
		},
		{
			name: "malformed length prefix",
			body: "zzzzcommand=fetch\n",
			want: false,
		},
		{
			name: "length exceeds body",
			body: "0099command=fetch\n",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProtocolV2Body([]byte(tt.body)); got != tt.want {
				t.Errorf("isProtocolV2Body(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestResolveGitProtocol(t *testing.T) {
	v2Body := []byte(pkt("command=fetch\n") + "0000")
	v0Body := []byte(pkt("want 74730d410fcb6603ace96f1dc55ea6196122532d\n") + "00000009done\n")

	t.Run("header present takes precedence", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/git-upload-pack", nil)
		r.Header.Set("Git-Protocol", "version=2")
		if got := resolveGitProtocol(r, v0Body); got != "version=2" {
			t.Errorf("got %q, want version=2 (header authoritative)", got)
		}
	})

	t.Run("header missing recovers v2 from body", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/git-upload-pack", nil)
		if got := resolveGitProtocol(r, v2Body); got != gitProtocolV2 {
			t.Errorf("got %q, want %q (recovered from body)", got, gitProtocolV2)
		}
	})

	t.Run("header missing and v0 body stays v0", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/git-upload-pack", nil)
		if got := resolveGitProtocol(r, v0Body); got != "" {
			t.Errorf("got %q, want empty (no v2 forced for v0 client)", got)
		}
	})
}

func TestGitProtocolEnvForBody(t *testing.T) {
	v2Body := []byte(pkt("command=fetch\n") + "0000")

	t.Run("recovered v2 sets GIT_PROTOCOL", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/git-upload-pack", nil)
		env := gitProtocolEnvForBody(r, v2Body)
		if !containsEnv(env, "GIT_PROTOCOL=version=2") {
			t.Errorf("expected GIT_PROTOCOL=version=2 in env, got %v", filterEnv(env))
		}
	})

	t.Run("v0 body without header leaves GIT_PROTOCOL unset", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/git-upload-pack", nil)
		env := gitProtocolEnvForBody(r, []byte("0000"))
		for _, e := range env {
			if len(e) >= 13 && e[:13] == "GIT_PROTOCOL=" {
				t.Errorf("expected no GIT_PROTOCOL, got %q", e)
			}
		}
	})
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func filterEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if len(e) >= 13 && e[:13] == "GIT_PROTOCOL=" {
			out = append(out, e)
		}
	}
	return out
}

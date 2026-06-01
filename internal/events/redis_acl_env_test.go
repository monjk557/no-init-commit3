package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApplyRedisACLEnv covers Redis ACL Phase B (Issue-3372):
//   - empty env keeps URL-derived credentials (pre-ACL compat / nopass default user)
//   - REDIS_USERNAME / REDIS_PASSWORD override when set
//   - username is provided alongside password so go-redis sends a two-argument AUTH
//
// applyRedisACLEnv treats an unset env var and an empty-string env var
// identically (os.Getenv returns "" for both), so cases use t.Setenv("") to
// represent "not injected".
func TestApplyRedisACLEnv(t *testing.T) {
	tests := []struct {
		name         string
		envUser      string
		envPass      string
		urlUser      string
		urlPass      string
		wantUser     string
		wantPassword string
	}{
		{
			name:         "empty env keeps url credentials (compat / nopass default)",
			wantUser:     "",
			wantPassword: "",
		},
		{
			name:         "env overrides url credentials",
			envUser:      "app",
			envPass:      "s3cret",
			wantUser:     "app",
			wantPassword: "s3cret",
		},
		{
			name:         "username trimmed; password preserved verbatim",
			envUser:      "  app  ",
			envPass:      " pad-kept ",
			wantUser:     "app",
			wantPassword: " pad-kept ",
		},
		{
			name:         "empty env values do not clobber url credentials",
			urlUser:      "urluser",
			urlPass:      "urlpass",
			wantUser:     "urluser",
			wantPassword: "urlpass",
		},
		{
			name:         "only password injected falls back to url username",
			envPass:      "s3cret",
			urlUser:      "urluser",
			wantUser:     "urluser",
			wantPassword: "s3cret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REDIS_USERNAME", tt.envUser)
			t.Setenv("REDIS_PASSWORD", tt.envPass)

			gotUser, gotPass := applyRedisACLEnv(tt.urlUser, tt.urlPass)
			assert.Equal(t, tt.wantUser, gotUser)
			assert.Equal(t, tt.wantPassword, gotPass)
		})
	}
}

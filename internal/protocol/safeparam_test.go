package protocol

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSafeParam_URLDecode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		paramVal string
		want     string
	}{
		{
			name:     "plain ASCII branch",
			paramVal: "main",
			want:     "main",
		},
		{
			name:     "Korean branch already decoded",
			paramVal: "SKY-06-redis-sdk추가",
			want:     "SKY-06-redis-sdk추가",
		},
		{
			name:     "Korean branch percent-encoded (double encoding scenario)",
			paramVal: "SKY-06-redis-sdk%EC%B6%94%EA%B0%80",
			want:     "SKY-06-redis-sdk추가",
		},
		{
			name:     "fully encoded Korean",
			paramVal: "%ED%95%9C%EA%B8%80%EB%B8%8C%EB%9E%9C%EC%B9%98",
			want:     "한글브랜치",
		},
		{
			name:     "slash-prefixed branch with encoding",
			paramVal: "/%EC%B6%94%EA%B0%80",
			want:     "/추가",
		},
		{
			name:     "branch with special chars (no encoding needed)",
			paramVal: "feature/my-branch",
			want:     "feature/my-branch",
		},
		{
			name:     "invalid percent encoding (passthrough)",
			paramVal: "%ZZinvalid",
			want:     "%ZZinvalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{
				{Key: "ref", Value: tt.paramVal},
			}

			got := SafeParam(c, "ref")
			if got != tt.want {
				t.Errorf("SafeParam(c, \"ref\") = %q, want %q", got, tt.want)
			}
		})
	}
}

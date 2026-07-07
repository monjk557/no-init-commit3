package gc

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
)

// routes_coverage_test.go — RegisterRoutes 를 spec-only(nil) + 실제 handler
// 양쪽 모드로 커버.
func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("spec-only nil handler registers without panic", func(t *testing.T) {
		r := gin.New()
		RegisterRoutes(r, nil)
	})

	t.Run("real handler binds the scan endpoint", func(t *testing.T) {
		root := t.TempDir()
		h, err := NewHandler(&config.Config{
			Logging: config.LoggingConfig{Level: "error", Format: "json"},
			Git:     config.GitConfig{RepoRoot: root},
		})
		if err != nil {
			t.Fatalf("NewHandler: %v", err)
		}
		r := gin.New()
		RegisterRoutes(r, h)

		found := false
		for _, ri := range r.Routes() {
			if ri.Path == "/lfs/reachable-oids" && ri.Method == "POST" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected POST /lfs/reachable-oids to be registered, routes=%v", r.Routes())
		}
	})
}

package context

import (
	stdctx "context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readworkflows_test.go — readWorkflows 를 직접 커버한다: yaml 읽기 성공,
// 비-yaml 파일 skip, invalid sha 에러 전파. (helper 들은 native_git_test.go /
// service_test.go 에 정의 — 같은 package.)

func TestReadWorkflows(t *testing.T) {
	svc := NewService(testLogger(t))
	ctx := stdctx.Background()

	t.Run("reads yaml workflows and skips non-yaml", func(t *testing.T) {
		// newRev 트리에 yaml 2개 + txt 1개를 심는다. readWorkflows 는 yaml 만
		// map 에 담고 txt 는 isWorkflowYAML skip 분기로 건너뛴다.
		repoPath, _, newRev := newRepoWithWorkflowSets(t,
			map[string]string{"ci.yaml": "name: ci\n"},
			map[string]string{
				"ci.yaml":    "name: ci\n",
				"deploy.yml": "name: deploy\n",
				"notes.txt":  "not a workflow\n",
			},
		)

		workflows, err := svc.readWorkflows(ctx, repoPath, newRev)
		require.NoError(t, err)
		assert.Equal(t, "name: ci\n", workflows["ci.yaml"])
		assert.Equal(t, "name: deploy\n", workflows["deploy.yml"])
		assert.NotContains(t, workflows, "notes.txt", "non-yaml file must be skipped")
	})

	t.Run("missing workflows dir returns empty map without error", func(t *testing.T) {
		repoPath, _, newRev := newRepoWithWorkflowSets(t,
			map[string]string{},
			map[string]string{}, // no .collabops dir at all
		)
		workflows, err := svc.readWorkflows(ctx, repoPath, newRev)
		require.NoError(t, err)
		assert.Empty(t, workflows)
	})

	t.Run("invalid sha propagates error", func(t *testing.T) {
		repoPath, _, _ := newRepoWithWorkflowSets(t,
			map[string]string{"ci.yaml": "name: ci\n"},
			map[string]string{"ci.yaml": "name: ci\n"},
		)
		_, err := svc.readWorkflows(ctx, repoPath, "deadbeef"+strings.Repeat("0", 32))
		assert.Error(t, err)
	})
}

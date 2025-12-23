package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestReceivePackInfoRefs_AdvertisesNoThin(t *testing.T) {
	tempDir := t.TempDir()

	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// HEAD -> refs/heads/main, and create a dummy ref so advertisement isn't empty.
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithOwnerRepo(
		"GET",
		"/test/repo.git/info/refs?service=git-receive-pack",
		owner,
		repoParam,
	)
	rr := httptest.NewRecorder()

	h.HandleInfoRefs(rr, req)

	require.Equal(t, 200, rr.Code)
	require.Equal(t, "application/x-git-receive-pack-advertisement", rr.Header().Get("Content-Type"))
	require.Contains(t, rr.Body.String(), "no-thin")
}

type fakePackfileStorer struct {
	storage.Storer
	closeErr error
}

func (s *fakePackfileStorer) PackfileWriter() (io.WriteCloser, error) {
	return &fakeWriteCloser{closeErr: s.closeErr}, nil
}

type fakeWriteCloser struct {
	bytes.Buffer
	closeErr error
}

func (w *fakeWriteCloser) Close() error { return w.closeErr }

func TestProcessPackfile_ThinPackTriggersUnthinFallback(t *testing.T) {
	// Minimal pack header for validation: "PACK" + version(2) + objectCount(0)
	var pack bytes.Buffer
	pack.WriteString("PACK")
	require.NoError(t, binary.Write(&pack, binary.BigEndian, uint32(2)))
	require.NoError(t, binary.Write(&pack, binary.BigEndian, uint32(0)))

	mem := memory.NewStorage()
	fake := &fakePackfileStorer{
		Storer:   mem,
		closeErr: packfile.ErrReferenceDeltaNotFound,
	}
	repo := &gogit.Repository{Storer: fake}

	called := false
	h := &ReceivePackHandler{
		unthinFallbackFn: func(_ *gogit.Repository, _ []byte) error {
			called = true
			return nil
		},
	}

	err := h.processPackfile(context.Background(), repo, pack.Bytes())
	require.NoError(t, err)
	require.True(t, called, "expected unthin fallback to be called")
}

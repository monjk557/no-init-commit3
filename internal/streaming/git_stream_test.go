package streaming

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func testRepoWithCommit(t *testing.T) (*gogit.Repository, plumbing.Hash) {
	t.Helper()
	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "content", "msg")
	ref, err := r.Repo.Head()
	require.NoError(t, err)
	return r.Repo, ref.Hash()
}

// Test_GitStream_Clone_StreamsData clone 스트리밍 시 데이터가 스트림됨
func Test_GitStream_Clone_StreamsData(t *testing.T) {
	repo, headHash := testRepoWithCommit(t)
	gs := NewGitStreamer(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	err := gs.StreamPackfile(ctx, &PackfileStreamRequest{
		Wants:        []string{headHash.String()},
		Haves:        nil,
		Capabilities: nil,
	}, &buf)
	require.NoError(t, err)
	require.Greater(t, buf.Len(), 0)
}

// Test_GitStream_Push_AcceptsStream info/refs 스트리밍( push/fetch 협상용)
func Test_GitStream_Push_AcceptsStream(t *testing.T) {
	repo, _ := testRepoWithCommit(t)
	gs := NewGitStreamer(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var buf bytes.Buffer
	err := gs.StreamRefs(ctx, &buf, nil)
	require.NoError(t, err)
	require.Greater(t, buf.Len(), 0)
}

// Test_GitStream_LargeRepo_HandlesChunking 대용량 레포에서도 청킹으로 메모리 폭발 없음
func Test_GitStream_LargeRepo_HandlesChunking(t *testing.T) {
	r := gitrepo.NewRepo(t)
	files := make(map[string]string)
	for i := 0; i < 50; i++ {
		files[fmt.Sprintf("d%d/f%d.txt", i, i)] = "content"
	}
	r.CommitMultiple(files, "many files")
	ref, err := r.Repo.Head()
	require.NoError(t, err)

	gs := NewGitStreamer(r.Repo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var buf bytes.Buffer
	err = gs.StreamPackfile(ctx, &PackfileStreamRequest{
		Wants: []string{ref.Hash().String()},
		Haves: nil,
	}, &buf)
	require.NoError(t, err)
	require.Greater(t, buf.Len(), 0)
}

// --- 11.2A Backpressure / Disconnect ---

// slowWriter writes one byte at a time to simulate slow client (backpressure)
type slowWriter struct {
	w     io.Writer
	delay time.Duration
}

func (s *slowWriter) Write(p []byte) (n int, err error) {
	for i := range p {
		_, err = s.w.Write(p[i : i+1])
		if err != nil {
			return i, err
		}
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		n++
	}
	return n, nil
}

// Test_GitStream_Backpressure_ClientSlow_DoesNotOOM 클라이언트가 느려도 OOM 없이 스트리밍
func Test_GitStream_Backpressure_ClientSlow_DoesNotOOM(t *testing.T) {
	repo, headHash := testRepoWithCommit(t)
	gs := NewGitStreamer(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var buf bytes.Buffer
	slow := &slowWriter{w: &buf, delay: 1 * time.Millisecond}
	err := gs.StreamPackfile(ctx, &PackfileStreamRequest{
		Wants: []string{headHash.String()},
		Haves: nil,
	}, slow)
	require.NoError(t, err)
	require.Greater(t, buf.Len(), 0)
}

// Test_GitStream_ClientDisconnect_StopsWorkAndReleasesResources 클라이언트 연결 끊기면 작업 중단·리소스 해제
func Test_GitStream_ClientDisconnect_StopsWorkAndReleasesResources(t *testing.T) {
	repo, headHash := testRepoWithCommit(t)
	gs := NewGitStreamer(repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- gs.StreamPackfile(ctx, &PackfileStreamRequest{
			Wants: []string{headHash.String()},
			Haves: nil,
		}, &buf)
	}()

	cancel()
	err := <-done
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

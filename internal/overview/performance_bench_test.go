package overview

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

type perfRepo struct {
	repoPath  string
	repo      *gogit.Repository
	refHash   plumbing.Hash
	rootFiles []string
}

func newOverviewServiceForBench(tb testing.TB, repoRoot string) *RepositoryOverviewService {
	tb.Helper()

	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "warn",
		Format:      "json",
		Output:      "stdout",
		Development: false,
	})
	require.NoError(tb, err)

	pathConfig, err := repositories.NewPathConfig(repoRoot)
	require.NoError(tb, err)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	require.NoError(tb, err)

	svc, err := NewRepositoryOverviewService(logger, pathConfig, repoManager)
	require.NoError(tb, err)
	return svc
}

func createBareRepo_S1_NHeavy(tb testing.TB, repoPath string, n int, c int) perfRepo {
	tb.Helper()
	require.GreaterOrEqual(tb, n, 1)
	require.GreaterOrEqual(tb, c, 1)

	require.NoError(tb, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(tb, err)

	rootFiles := make([]string, 0, n)
	entries := make([]object.TreeEntry, 0, n)

	// Small-ish per file content; N-heavy focuses on entry count and overview loops.
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%05d.txt", i)
		rootFiles = append(rootFiles, name)

		content := make([]byte, 64)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)

		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	treeObj := &object.Tree{Entries: entries}

	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(tb, treeObj.Encode(treeEnc))
	treeHash, err := repo.Storer.SetEncodedObject(treeEnc)
	require.NoError(tb, err)

	when := time.Unix(1700000000, 0).UTC()

	// Commit chain of length C; keep the same tree hash so the latest change for pending files stays at the initial commit.
	var parent plumbing.Hash
	for i := 0; i < c; i++ {
		commit := &object.Commit{
			Message:   fmt.Sprintf("commit %d", i),
			Author:    object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(i) * time.Second)},
			Committer: object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(i) * time.Second)},
			TreeHash:  treeHash,
		}
		if i > 0 {
			commit.ParentHashes = []plumbing.Hash{parent}
		}

		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(tb, err)
		parent = commitHash
	}

	require.NoError(tb, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), parent)))
	require.NoError(tb, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))))

	return perfRepo{
		repoPath:  repoPath,
		repo:      repo,
		refHash:   parent,
		rootFiles: rootFiles,
	}
}

// createBareRepo_S3_FHeavy creates a repo where "pending paths" are root files that last changed long ago,
// while each commit changes many files under a noise directory to make DiffTree produce large changes (F-heavy).
//
// NOTE: This scenario is intended for Benchmark_GetLatestCommitsOnly to avoid "noise" directory being included
// as a pending tree path by GetTreeWithCommit.
func createBareRepo_S3_FHeavy(tb testing.TB, repoPath string, pendingN int, c int, f int) (perfRepo, []CommitPathInfo) {
	tb.Helper()
	require.GreaterOrEqual(tb, pendingN, 1)
	require.GreaterOrEqual(tb, c, 2)
	require.GreaterOrEqual(tb, f, 1)

	require.NoError(tb, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(tb, err)

	// Root pending files.
	rootFiles := make([]string, 0, pendingN)
	rootEntries := make([]object.TreeEntry, 0, pendingN+1)
	for i := 0; i < pendingN; i++ {
		name := fmt.Sprintf("p%05d.txt", i)
		rootFiles = append(rootFiles, name)

		content := make([]byte, 64)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)

		rootEntries = append(rootEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	// Noise files live under a directory to avoid being part of pending set in LatestCommitsOnly benchmark.
	noiseTotal := f
	noiseEntries := make([]object.TreeEntry, 0, noiseTotal)
	for i := 0; i < noiseTotal; i++ {
		name := fmt.Sprintf("n%05d.bin", i)
		content := make([]byte, 256)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)
		noiseEntries = append(noiseEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	sort.Slice(noiseEntries, func(i, j int) bool { return noiseEntries[i].Name < noiseEntries[j].Name })
	noiseTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(tb, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
	noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
	require.NoError(tb, err)

	rootEntries = append(rootEntries, object.TreeEntry{Name: "noise", Mode: filemode.Dir, Hash: noiseTreeHash})
	sort.Slice(rootEntries, func(i, j int) bool { return rootEntries[i].Name < rootEntries[j].Name })

	rootTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(tb, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
	rootTreeHash, err := repo.Storer.SetEncodedObject(rootTreeEnc)
	require.NoError(tb, err)

	when := time.Unix(1700000000, 0).UTC()

	var parent plumbing.Hash
	// Commit 0: create everything.
	{
		commit := &object.Commit{
			Message:   "init",
			Author:    object.Signature{Name: "bench", Email: "bench@example.com", When: when},
			Committer: object.Signature{Name: "bench", Email: "bench@example.com", When: when},
			TreeHash:  rootTreeHash,
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(tb, err)
		parent = commitHash
	}

	// Commits 1..C-1: only change noise blobs (F changes per commit), keep pending root files untouched.
	for ci := 1; ci < c; ci++ {
		// Update all noise blobs each commit to force F changes.
		for i := 0; i < noiseTotal; i++ {
			content := make([]byte, 256)
			binary.LittleEndian.PutUint64(content[:8], uint64(ci))
			binary.LittleEndian.PutUint64(content[8:16], uint64(i))

			blob := repo.Storer.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			blob.SetSize(int64(len(content)))
			w, err := blob.Writer()
			require.NoError(tb, err)
			_, err = w.Write(content)
			require.NoError(tb, err)
			require.NoError(tb, w.Close())

			blobHash, err := repo.Storer.SetEncodedObject(blob)
			require.NoError(tb, err)
			noiseEntries[i].Hash = blobHash
		}

		noiseTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
		noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
		require.NoError(tb, err)

		// Root tree: same root pending file entries, updated noise subtree hash.
		for i := range rootEntries {
			if rootEntries[i].Name == "noise" {
				rootEntries[i].Hash = noiseTreeHash
				break
			}
		}
		rootTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
		rootTreeHash, err = repo.Storer.SetEncodedObject(rootTreeEnc)
		require.NoError(tb, err)

		commit := &object.Commit{
			Message:      fmt.Sprintf("commit %d", ci),
			Author:       object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			Committer:    object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			TreeHash:     rootTreeHash,
			ParentHashes: []plumbing.Hash{parent},
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(tb, err)
		parent = commitHash
	}

	require.NoError(tb, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), parent)))
	require.NoError(tb, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))))

	pathInfos := make([]CommitPathInfo, 0, len(rootFiles))
	for _, p := range rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	return perfRepo{
		repoPath:  repoPath,
		repo:      repo,
		refHash:   parent,
		rootFiles: rootFiles,
	}, pathInfos
}

// createBareRepo_S2_CHeavy creates a repo where pending root files were last changed very early,
// while a long commit history follows with small per-commit diffs (F small) that do not touch pending paths.
// This stresses "commit scan length (C)" and early-stop behavior.
func createBareRepo_S2_CHeavy(tb testing.TB, repoPath string, pendingN int, c int, f int) (perfRepo, []CommitPathInfo) {
	tb.Helper()
	require.GreaterOrEqual(tb, pendingN, 1)
	require.GreaterOrEqual(tb, c, 2)
	require.GreaterOrEqual(tb, f, 1)

	require.NoError(tb, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(tb, err)

	// Root pending files.
	rootFiles := make([]string, 0, pendingN)
	rootEntries := make([]object.TreeEntry, 0, pendingN+1)
	for i := 0; i < pendingN; i++ {
		name := fmt.Sprintf("p%05d.txt", i)
		rootFiles = append(rootFiles, name)

		content := make([]byte, 64)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)

		rootEntries = append(rootEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	// Noise subtree with F files (small).
	noiseEntries := make([]object.TreeEntry, 0, f)
	for i := 0; i < f; i++ {
		name := fmt.Sprintf("n%05d.bin", i)
		content := make([]byte, 256)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)
		noiseEntries = append(noiseEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	sort.Slice(noiseEntries, func(i, j int) bool { return noiseEntries[i].Name < noiseEntries[j].Name })
	noiseTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(tb, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
	noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
	require.NoError(tb, err)

	rootEntries = append(rootEntries, object.TreeEntry{Name: "noise", Mode: filemode.Dir, Hash: noiseTreeHash})
	sort.Slice(rootEntries, func(i, j int) bool { return rootEntries[i].Name < rootEntries[j].Name })

	rootTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(tb, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
	rootTreeHash, err := repo.Storer.SetEncodedObject(rootTreeEnc)
	require.NoError(tb, err)

	when := time.Unix(1700000000, 0).UTC()

	var parent plumbing.Hash
	// Commit 0: create everything.
	{
		commit := &object.Commit{
			Message:   "init",
			Author:    object.Signature{Name: "bench", Email: "bench@example.com", When: when},
			Committer: object.Signature{Name: "bench", Email: "bench@example.com", When: when},
			TreeHash:  rootTreeHash,
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(tb, err)
		parent = commitHash
	}

	// Commits 1..C-1: touch exactly 1 noise blob per commit (keep F small overall, but ensure there is at least one change).
	for ci := 1; ci < c; ci++ {
		idx := (ci - 1) % len(noiseEntries)
		content := make([]byte, 256)
		binary.LittleEndian.PutUint64(content[:8], uint64(ci))
		binary.LittleEndian.PutUint64(content[8:16], uint64(idx))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(tb, err)
		_, err = w.Write(content)
		require.NoError(tb, err)
		require.NoError(tb, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(tb, err)
		noiseEntries[idx].Hash = blobHash

		noiseTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
		noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
		require.NoError(tb, err)

		for i := range rootEntries {
			if rootEntries[i].Name == "noise" {
				rootEntries[i].Hash = noiseTreeHash
				break
			}
		}
		rootTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
		rootTreeHash, err = repo.Storer.SetEncodedObject(rootTreeEnc)
		require.NoError(tb, err)

		commit := &object.Commit{
			Message:      fmt.Sprintf("commit %d", ci),
			Author:       object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			Committer:    object.Signature{Name: "bench", Email: "bench@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			TreeHash:     rootTreeHash,
			ParentHashes: []plumbing.Hash{parent},
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(tb, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(tb, err)
		parent = commitHash
	}

	require.NoError(tb, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), parent)))
	require.NoError(tb, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))))

	pathInfos := make([]CommitPathInfo, 0, len(rootFiles))
	for _, p := range rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	return perfRepo{repoPath: repoPath, repo: repo, refHash: parent, rootFiles: rootFiles}, pathInfos
}

func Benchmark_GetTreeOnly_S1_NHeavy(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 2000, 200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		refObj, err := pr.repo.ResolveRevision(plumbing.Revision(pr.refHash.String()))
		if err != nil {
			b.Fatal(err)
		}
		commit, err := pr.repo.CommitObject(*refObj)
		if err != nil {
			b.Fatal(err)
		}
		treeObj, err := commit.Tree()
		if err != nil {
			b.Fatal(err)
		}

		_ = ctx
		_ = treeObj.Entries
	}
}

func Benchmark_BlobSizeFillOnly_S1_NHeavy(b *testing.B) {
	repoRoot := b.TempDir()
	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 2000, 200)

	refObj, err := pr.repo.ResolveRevision(plumbing.Revision(pr.refHash.String()))
	if err != nil {
		b.Fatal(err)
	}
	commit, err := pr.repo.CommitObject(*refObj)
	if err != nil {
		b.Fatal(err)
	}
	treeObj, err := commit.Tree()
	if err != nil {
		b.Fatal(err)
	}

	var blobHashes []plumbing.Hash
	for _, e := range treeObj.Entries {
		if e.Mode == filemode.Regular {
			blobHashes = append(blobHashes, e.Hash)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, h := range blobHashes {
			blob, err := pr.repo.BlobObject(h)
			if err != nil {
				b.Fatal(err)
			}
			_ = blob.Size
		}
	}
}

func Benchmark_BlobSizeFillOnly_S1_NHeavy_Parallel(b *testing.B) {
	repoRoot := b.TempDir()
	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 2000, 200)

	refObj, err := pr.repo.ResolveRevision(plumbing.Revision(pr.refHash.String()))
	if err != nil {
		b.Fatal(err)
	}
	commit, err := pr.repo.CommitObject(*refObj)
	if err != nil {
		b.Fatal(err)
	}
	treeObj, err := commit.Tree()
	if err != nil {
		b.Fatal(err)
	}

	var blobHashes []plumbing.Hash
	for _, e := range treeObj.Entries {
		if e.Mode == filemode.Regular {
			blobHashes = append(blobHashes, e.Hash)
		}
	}

	parallelism := 8
	sem := make(chan struct{}, parallelism)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for _, h := range blobHashes {
			wg.Add(1)
			sem <- struct{}{}
			go func(hash plumbing.Hash) {
				defer wg.Done()
				defer func() { <-sem }()
				blob, err := pr.repo.BlobObject(hash)
				if err != nil {
					return
				}
				_ = blob.Size
			}(h)
		}
		wg.Wait()
	}
}

func Benchmark_GetLatestCommitsOnly_S1_NHeavy(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 2000, 200)
	svc := newOverviewServiceForBench(b, repoRoot)

	pathInfos := make([]CommitPathInfo, 0, len(pr.rootFiles))
	for _, p := range pr.rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	ref := pr.refHash.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.GetLatestCommitsForPaths(ctx, pr.repo, ref, pathInfos)
	}
}

func Benchmark_GetLatestCommitsOnly_S3_FHeavy(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	// Keep numbers moderate for local runs; scale up as needed.
	pr, pathInfos := createBareRepo_S3_FHeavy(b, filepath.Join(repoRoot, "perf", "s3.git"), 200, 500, 500)
	svc := newOverviewServiceForBench(b, repoRoot)

	ref := pr.refHash.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.GetLatestCommitsForPaths(ctx, pr.repo, ref, pathInfos)
	}
}

func Benchmark_GetLatestCommitsOnly_S2_CHeavy(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	// Keep numbers moderate for local runs; scale up if needed.
	pr, pathInfos := createBareRepo_S2_CHeavy(b, filepath.Join(repoRoot, "perf", "s2.git"), 200, 2000, 10)
	svc := newOverviewServiceForBench(b, repoRoot)

	ref := pr.refHash.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.GetLatestCommitsForPaths(ctx, pr.repo, ref, pathInfos)
	}
}

func Benchmark_GetTreeWithCommit_Full_S1_NHeavy(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 2000, 200)
	svc := newOverviewServiceForBench(b, repoRoot)

	// Keep ref fixed (not HEAD) for stable comparisons.
	ref := pr.refHash.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := svc.GetTreeWithCommit(ctx, pr.repo, ref, "")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func Benchmark_LatestCommitsCaps_S1_NHeavy_Uncapped(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 10_000, 200)
	svc := newOverviewServiceForBench(b, repoRoot)
	pathInfos := make([]CommitPathInfo, 0, len(pr.rootFiles))
	for _, p := range pr.rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 0, 0)
	}
}

func Benchmark_LatestCommitsCaps_S1_NHeavy_TimeBudget50ms(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 10_000, 200)
	svc := newOverviewServiceForBench(b, repoRoot)
	pathInfos := make([]CommitPathInfo, 0, len(pr.rootFiles))
	for _, p := range pr.rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 50*time.Millisecond, 1_000_000)
	}
}

func Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit(b *testing.B) {
	// Cache warm hit benchmark: shows how much I/O disappears when results are served from in-memory cache.
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr := createBareRepo_S1_NHeavy(b, filepath.Join(repoRoot, "perf", "s1.git"), 10_000, 200)
	svc := newOverviewServiceForBench(b, repoRoot)
	pathInfos := make([]CommitPathInfo, 0, len(pr.rootFiles))
	for _, p := range pr.rootFiles {
		pathInfos = append(pathInfos, CommitPathInfo{Path: p, IsTree: false})
	}

	cache := NewOverviewCache(100, 30*time.Second)
	cacheKey := "root|" + pr.refHash.String() + "|paths_hash_placeholder"

	// Compute once (miss) and cache it.
	commits, meta := svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 0, 0)
	if meta.Incomplete {
		b.Fatalf("expected complete result to populate cache")
	}
	cache.SetLatestCommitsBatch(pr.repoPath, pr.refHash.String(), cacheKey, commits)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := cache.GetLatestCommitsBatch(pr.repoPath, pr.refHash.String(), cacheKey); !ok {
			b.Fatalf("expected cache hit")
		}
	}
}

func Benchmark_LatestCommitsCaps_S2_CHeavy_Uncapped(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr, pathInfos := createBareRepo_S2_CHeavy(b, filepath.Join(repoRoot, "perf", "s2.git"), 200, 2000, 10)
	svc := newOverviewServiceForBench(b, repoRoot)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 0, 0)
	}
}

func Benchmark_LatestCommitsCaps_S2_CHeavy_TimeBudget50ms(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr, pathInfos := createBareRepo_S2_CHeavy(b, filepath.Join(repoRoot, "perf", "s2.git"), 200, 2000, 10)
	svc := newOverviewServiceForBench(b, repoRoot)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 50*time.Millisecond, 1_000_000)
	}
}

func Benchmark_LatestCommitsCaps_S3_FHeavy_Small_Uncapped(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	// Smaller than the default S3 benchmark for practical iteration.
	pr, pathInfos := createBareRepo_S3_FHeavy(b, filepath.Join(repoRoot, "perf", "s3_small.git"), 50, 200, 200)
	svc := newOverviewServiceForBench(b, repoRoot)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 0, 0)
	}
}

func Benchmark_LatestCommitsCaps_S3_FHeavy_Small_TimeBudget50ms(b *testing.B) {
	ctx := context.Background()
	repoRoot := b.TempDir()

	pr, pathInfos := createBareRepo_S3_FHeavy(b, filepath.Join(repoRoot, "perf", "s3_small.git"), 50, 200, 200)
	svc := newOverviewServiceForBench(b, repoRoot)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetLatestCommitsForPathsWithMeta(ctx, pr.repo, pr.refHash, pathInfos, 50*time.Millisecond, 1_000_000)
	}
}

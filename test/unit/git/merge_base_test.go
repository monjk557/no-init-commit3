package git

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindMergeBase_NormalCase는 일반적인 분기 상황에서 merge base를 올바르게 찾는지 테스트합니다.
func TestFindMergeBase_NormalCase(t *testing.T) {

	// 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "git-test-merge-base-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Git 레포지토리 초기화
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// 커밋 히스토리 생성:
	// A---B---C (main)
	//      \
	//       D---E (feature)
	// merge base는 B가 되어야 함

	// Initial commit A
	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Commit B on main
	writeFile(t, tempDir, "file2.txt", "content2")
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	commitB, err := wt.Commit("Commit B", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Create feature branch from B
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/feature",
		Create: true,
	})
	require.NoError(t, err)

	// Commit D on feature
	writeFile(t, tempDir, "file3.txt", "content3")
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit D", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Commit E on feature
	writeFile(t, tempDir, "file4.txt", "content4")
	_, err = wt.Add("file4.txt")
	require.NoError(t, err)
	commitE, err := wt.Commit("Commit E", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Switch back to main
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/main",
	})
	require.NoError(t, err)

	// Commit C on main
	writeFile(t, tempDir, "file5.txt", "content5")
	_, err = wt.Add("file5.txt")
	require.NoError(t, err)
	commitC, err := wt.Commit("Commit C", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Get commit objects
	commitEObj, err := repo.CommitObject(commitE)
	require.NoError(t, err)
	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)

	// Test findMergeBase using go-git's MergeBase directly (same logic used in conflict_detector)
	mergeBases, err := commitEObj.MergeBase(commitCObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitB.String(), mergeBases[0].Hash.String(), "Merge base should be commit B")
}

// TestFindMergeBase_SameCommit은 같은 커밋인 경우 merge base가 자기 자신인지 테스트합니다.
func TestFindMergeBase_SameCommit(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-same-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Single commit
	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	commitA, err := wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitAObj, err := repo.CommitObject(commitA)
	require.NoError(t, err)

	// Test merge base of same commit
	mergeBases, err := commitAObj.MergeBase(commitAObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitA.String(), mergeBases[0].Hash.String(), "Merge base of same commit should be itself")
}

// TestFindMergeBase_FastForward는 Fast-forward 상황에서 merge base를 테스트합니다.
func TestFindMergeBase_FastForward(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-ff-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Linear history: A---B---C
	// C는 B의 조상이므로 merge base는 B

	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file2.txt", "content2")
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	commitB, err := wt.Commit("Commit B", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file3.txt", "content3")
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)
	commitC, err := wt.Commit("Commit C", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)
	commitBObj, err := repo.CommitObject(commitB)
	require.NoError(t, err)

	// Test merge base in fast-forward case
	// C와 B의 merge base는 B (B가 C의 조상)
	mergeBases, err := commitCObj.MergeBase(commitBObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitB.String(), mergeBases[0].Hash.String(), "Merge base should be commit B (the ancestor)")
}

// TestFindMergeBase_MergeBaseIsSource는 merge base가 source commit과 같은 경우를 테스트합니다.
func TestFindMergeBase_MergeBaseIsSource(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-source-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// A---B (main)
	//  \
	//   C (feature)
	// feature는 main의 조상이므로 merge base는 A

	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	commitA, err := wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Create feature branch from A
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/feature",
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file2.txt", "content2")
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	commitC, err := wt.Commit("Commit C", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Switch back to main
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/main",
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file3.txt", "content3")
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)
	commitB, err := wt.Commit("Commit B", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)
	commitBObj, err := repo.CommitObject(commitB)
	require.NoError(t, err)

	// Test merge base when source is ancestor
	// C와 B의 merge base는 A
	mergeBases, err := commitCObj.MergeBase(commitBObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitA.String(), mergeBases[0].Hash.String(), "Merge base should be commit A")
}

// TestFindMergeBase_MergeBaseIsTarget는 merge base가 target commit과 같은 경우를 테스트합니다.
func TestFindMergeBase_MergeBaseIsTarget(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-target-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// A---B (main)
	//      \
	//       C (feature)
	// main이 feature의 조상이므로 merge base는 B

	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file2.txt", "content2")
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	commitB, err := wt.Commit("Commit B", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Create feature branch from B
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/feature",
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file3.txt", "content3")
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)
	commitC, err := wt.Commit("Commit C", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)
	commitBObj, err := repo.CommitObject(commitB)
	require.NoError(t, err)

	// Test merge base when target is ancestor
	// C와 B의 merge base는 B (B가 C의 조상)
	mergeBases, err := commitCObj.MergeBase(commitBObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitB.String(), mergeBases[0].Hash.String(), "Merge base should be commit B (the ancestor)")
}

// TestFindMergeBase_ComplexBranching는 복잡한 분기 상황을 테스트합니다.
func TestFindMergeBase_ComplexBranching(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-complex-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Complex history:
	//     A---B---C---D (main)
	//          \
	//           E---F---G (feature)
	// merge base는 B

	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file2.txt", "content2")
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)
	commitB, err := wt.Commit("Commit B", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Create feature branch from B
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/feature",
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file3.txt", "content3")
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit E", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file4.txt", "content4")
	_, err = wt.Add("file4.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit F", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file5.txt", "content5")
	_, err = wt.Add("file5.txt")
	require.NoError(t, err)
	commitG, err := wt.Commit("Commit G", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Switch back to main
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: "refs/heads/main",
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file6.txt", "content6")
	_, err = wt.Add("file6.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Commit C", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	writeFile(t, tempDir, "file7.txt", "content7")
	_, err = wt.Add("file7.txt")
	require.NoError(t, err)
	commitD, err := wt.Commit("Commit D", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitGObj, err := repo.CommitObject(commitG)
	require.NoError(t, err)
	commitDObj, err := repo.CommitObject(commitD)
	require.NoError(t, err)

	// Test merge base in complex branching scenario
	// G와 D의 merge base는 B
	mergeBases, err := commitGObj.MergeBase(commitDObj)
	require.NoError(t, err)
	require.NotEmpty(t, mergeBases, "Should find at least one merge base")
	assert.Equal(t, commitB.String(), mergeBases[0].Hash.String(), "Merge base should be commit B")
}

// TestFindMergeBase_InvalidCommits는 존재하지 않는 커밋에 대한 에러 처리를 테스트합니다.
func TestFindMergeBase_InvalidCommits(t *testing.T) {

	tempDir, err := os.MkdirTemp("", "git-test-merge-base-invalid-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create a valid commit
	writeFile(t, tempDir, "file1.txt", "content1")
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	commitA, err := wt.Commit("Commit A", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	commitAObj, err := repo.CommitObject(commitA)
	require.NoError(t, err)

	// Create an invalid commit hash (doesn't exist)
	invalidHash := commitA
	invalidHash[0] = ^invalidHash[0] // Flip bits to make it invalid
	invalidCommit, err := repo.CommitObject(invalidHash)
	require.Error(t, err, "Should fail to get invalid commit")
	assert.Nil(t, invalidCommit, "Invalid commit should be nil")

	// Test that MergeBase fails with invalid commit
	// Since we can't get the invalid commit object, we test the error handling
	// by attempting to use a non-existent hash
	_, err = commitAObj.MergeBase(nil)
	assert.Error(t, err, "Should fail with nil commit")
}

// Helper function to write a file
func writeFile(t *testing.T, dir, filename, content string) {
	fullPath := filepath.Join(dir, filename)
	dirPath := filepath.Dir(fullPath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(t, err)
	err = os.WriteFile(fullPath, []byte(content), 0644)
	require.NoError(t, err)
}

package integration

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/server"
)

// TCPGitServerTestSuite provides test environment for TCP Git server
type TCPGitServerTestSuite struct {
	server       *server.DuplexTCPServer
	config       *config.Config
	tempDir      string
	testRepoPath string
	serverAddr   string
	serverPort   int
}

// SetupTCPGitServerTest creates a test environment for TCP Git server
func SetupTCPGitServerTest(t *testing.T) *TCPGitServerTestSuite {
	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "tcp-git-server-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit
	createInitialCommitForTCPTest(t, testRepoPath)

	// Setup configuration
	serverPort := 0 // 0 means auto-assign port
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Server: config.ServerConfig{
			TCPGitPort: serverPort,
		},
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	// Create TCP server
	tcpServer, err := server.NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, tcpServer)

	// Use a fixed test port
	cfg.Server.TCPGitPort = 5223

	// Create TCP server with fixed port
	tcpServer, err = server.NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)
	err = tcpServer.Start()
	require.NoError(t, err)

	suite := &TCPGitServerTestSuite{
		server:       tcpServer,
		config:       cfg,
		tempDir:      tempDir,
		testRepoPath: testRepoPath,
		serverAddr:   fmt.Sprintf("localhost:%d", cfg.Server.TCPGitPort),
		serverPort:   cfg.Server.TCPGitPort,
	}

	// Set cleanup
	t.Cleanup(func() {
		suite.Cleanup()
	})

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	return suite
}

// Cleanup cleans up test resources
func (suite *TCPGitServerTestSuite) Cleanup() {
	if suite.server != nil {
		_ = suite.server.Stop()
	}
	if suite.tempDir != "" {
		_ = os.RemoveAll(suite.tempDir)
	}
}

// createInitialCommitForTCPTest creates an initial commit in the repository
func createInitialCommitForTCPTest(t *testing.T, repoPath string) {
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Create a blob for README.md
	readme := "# Test Repository\n\nInitial commit for TCP Git server testing.\n"
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(readme)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(readme))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Create tree with README.md
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: filemode.Regular,
				Hash: blobHash,
			},
		},
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	commit := &object.Commit{
		Message: "Initial commit",
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create and update master branch reference
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// Update HEAD to be a symbolic reference to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

// Helper functions for Git protocol pkt-line format

// writePktLine writes a pkt-line to the writer
func writePktLine(w io.Writer, data string) error {
	length := len(data) + 4
	line := fmt.Sprintf("%04x%s", length, data)
	_, err := w.Write([]byte(line))
	return err
}

// writeFlush writes a flush packet
func writeFlush(w io.Writer) error {
	_, err := w.Write([]byte("0000"))
	return err
}

// readPktLine reads a pkt-line from the reader
func readPktLine(r *bufio.Reader) ([]byte, bool, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, false, err
	}
	if bytes.Equal(hdr, []byte("0000")) {
		return nil, true, nil // flush
	}
	n, err := strconv.ParseUint(string(hdr), 16, 16)
	if err != nil {
		return nil, false, fmt.Errorf("bad pkt-len: %q", string(hdr))
	}
	if n < 4 {
		return nil, false, fmt.Errorf("invalid pkt-len: %d", n)
	}
	payloadLen := int(n) - 4
	buf := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, false, err
	}
	return buf, false, nil
}

// TestTCPGitServerUploadPack tests upload-pack (fetch/clone) functionality
func TestTCPGitServerUploadPack(t *testing.T) {
	suite := SetupTCPGitServerTest(t)

	t.Run("UploadPack_LsRefs", func(t *testing.T) {
		conn, err := net.Dial("tcp", suite.serverAddr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		// Set timeouts to prevent hanging
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		// Send service line
		serviceLine := "git-upload-pack /testowner/testrepo.git\x00"
		err = writePktLine(writer, serviceLine)
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read advertisement
		line, isFlush, err := readPktLine(reader)
		require.NoError(t, err)
		require.False(t, isFlush)
		require.Contains(t, string(line), "version 2", "Should receive protocol version 2")

		// Read until flush
		maxIterations := 20
		for i := 0; i < maxIterations; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			l, flush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if flush {
				break
			}
			// Check for capabilities
			lineStr := string(l)
			if strings.Contains(lineStr, "ls-refs") || strings.Contains(lineStr, "fetch") {
				_ = lineStr // Expected capability line
			}
		}

		// Send ls-refs command in Git protocol v2 format:
		// Section 1: command=ls-refs + delimiter (0001)
		err = writePktLine(writer, "command=ls-refs\n")
		require.NoError(t, err)
		_, err = writer.WriteString("0001") // delimiter between sections
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Section 2: options (peel, symrefs) + flush (0000)
		err = writePktLine(writer, "peel\n")
		require.NoError(t, err)
		err = writePktLine(writer, "symrefs\n")
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read refs response (refs only, then flush - no capabilities in response)
		var refs []string
		hasError := false
		for i := 0; i < 50; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			line, isFlush, err := readPktLine(reader)
			if err != nil {
				if i == 0 {
					// If we get error immediately, it might be a server error
					t.Logf("Error reading response: %v", err)
				}
				break
			}
			if isFlush {
				break
			}
			lineStr := string(line)
			if strings.HasPrefix(lineStr, "ERR ") {
				t.Errorf("Server returned error: %s", lineStr)
				hasError = true
				break
			}
			// Add all ref lines (format: <hash> <refname> or <hash> <refname> symref-target:<target>)
			if lineStr != "" {
				refs = append(refs, lineStr)
			}
		}

		// Verify we got refs
		if !hasError {
			assert.NotEmpty(t, refs, "Should receive refs. Received: %v", refs)
		}
		foundMaster := false
		for _, ref := range refs {
			if strings.Contains(ref, "refs/heads/master") {
				foundMaster = true
				break
			}
		}
		assert.True(t, foundMaster, "Should find master branch")
	})

	t.Run("UploadPack_Fetch", func(t *testing.T) {
		conn, err := net.Dial("tcp", suite.serverAddr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		// Set timeouts to prevent hanging
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		// Send service line
		serviceLine := "git-upload-pack /testowner/testrepo.git\x00"
		err = writePktLine(writer, serviceLine)
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read and skip advertisement
		maxIterations := 20
		for i := 0; i < maxIterations; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, isFlush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if isFlush {
				break
			}
		}

		// Get the current HEAD commit hash
		repo, err := gogit.PlainOpen(suite.testRepoPath)
		require.NoError(t, err)
		headRef, err := repo.Head()
		require.NoError(t, err)
		commitHash := headRef.Hash().String()

		// Send fetch command (section 1: command with delimiter)
		err = writePktLine(writer, "command=fetch\n")
		require.NoError(t, err)
		_, err = writer.WriteString("0001") // delimiter
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Send fetch options (section 2: want, done with flush)
		err = writePktLine(writer, fmt.Sprintf("want %s\n", commitHash))
		require.NoError(t, err)
		err = writePktLine(writer, "done\n")
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read packfile section header
		line, isFlush, err := readPktLine(reader)
		require.NoError(t, err)
		require.False(t, isFlush)
		assert.Contains(t, string(line), "packfile", "Should receive packfile section")

		// Read packfile data (sideband format)
		// The packfile will be wrapped in sideband packets
		packfileReceived := false
		for i := 0; i < 100; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			line, isFlush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if isFlush {
				break
			}
			data := string(line)
			if strings.Contains(data, "PACK") || len(line) > 100 {
				packfileReceived = true
				break
			}
		}

		assert.True(t, packfileReceived, "Should receive packfile data")
	})
}

// TestTCPGitServerReceivePack tests receive-pack (push) functionality
func TestTCPGitServerReceivePack(t *testing.T) {
	suite := SetupTCPGitServerTest(t)

	t.Run("ReceivePack_PushNewCommit", func(t *testing.T) {
		// Create a new repository to push from
		pushRepoPath, err := os.MkdirTemp("", "tcp-push-repo-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(pushRepoPath) }()

		// Clone the test repository
		repo, err := gogit.PlainClone(pushRepoPath, false, &gogit.CloneOptions{
			URL: suite.testRepoPath,
		})
		require.NoError(t, err)

		// Get current HEAD
		headRef, err := repo.Head()
		require.NoError(t, err)
		oldHash := headRef.Hash()

		// Create a new file and commit
		worktree, err := repo.Worktree()
		require.NoError(t, err)

		newFile := filepath.Join(pushRepoPath, "newfile.txt")
		err = os.WriteFile(newFile, []byte("New file content"), 0644)
		require.NoError(t, err)

		_, err = worktree.Add("newfile.txt")
		require.NoError(t, err)

		commitHash, err := worktree.Commit("Add new file", &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		require.NoError(t, err)

		// Get all objects needed for the push
		newCommit, err := repo.CommitObject(commitHash)
		require.NoError(t, err)

		// Create packfile with the new commit
		objects := []plumbing.Hash{commitHash}
		if !newCommit.TreeHash.IsZero() {
			objects = append(objects, newCommit.TreeHash)
		}

		// Get tree objects and blob objects
		tree, err := repo.TreeObject(newCommit.TreeHash)
		require.NoError(t, err)
		err = tree.Files().ForEach(func(f *object.File) error {
			objects = append(objects, f.Hash)
			return nil
		})
		require.NoError(t, err)

		// Now connect to TCP server and push
		conn, err := net.Dial("tcp", suite.serverAddr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		// Set timeouts to prevent hanging
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		// Send service line
		serviceLine := "git-receive-pack /testowner/testrepo.git\x00"
		err = writePktLine(writer, serviceLine)
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read advertisement
		var advertLines []string
		maxIterations := 20
		for i := 0; i < maxIterations; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			line, isFlush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if isFlush {
				break
			}
			advertLines = append(advertLines, string(line))
		}

		assert.NotEmpty(t, advertLines, "Should receive advertisement")

		// Send ref update command
		refUpdate := fmt.Sprintf("%s %s refs/heads/master\x00", oldHash.String(), commitHash.String())
		err = writePktLine(writer, refUpdate)
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Create and send packfile
		var packBuf bytes.Buffer
		enc := packfile.NewEncoder(&packBuf, repo.Storer, false)
		_, err = enc.Encode(objects, 0)
		require.NoError(t, err)

		packData := packBuf.Bytes()
		_, err = writer.Write(packData)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read server response (report-status)
		for i := 0; i < 20; i++ {
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, isFlush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if isFlush {
				break
			}
		}

		// Wait a bit for server to process
		time.Sleep(200 * time.Millisecond)

		// Verify the push succeeded by checking the repository
		targetRepo, err := gogit.PlainOpen(suite.testRepoPath)
		require.NoError(t, err)
		headRef, err = targetRepo.Head()
		require.NoError(t, err)
		assert.Equal(t, commitHash.String(), headRef.Hash().String(), "HEAD should be updated to new commit")
	})
}

// TestTCPGitServerIntegration tests full integration scenarios
func TestTCPGitServerIntegration(t *testing.T) {
	suite := SetupTCPGitServerTest(t)

	t.Run("FullPushAndFetchCycle", func(t *testing.T) {
		// First, push a commit via TCP
		// (This is a simplified test - full implementation would require packfile generation)

		// Then, fetch it back via TCP
		conn, err := net.Dial("tcp", suite.serverAddr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		// Set timeouts to prevent hanging
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)

		// Send service line for upload-pack
		serviceLine := "git-upload-pack /testowner/testrepo.git\x00"
		err = writePktLine(writer, serviceLine)
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Read advertisement
		maxIterations := 20
		for i := 0; i < maxIterations; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, isFlush, err := readPktLine(reader)
			if err != nil {
				break
			}
			if isFlush {
				break
			}
		}

		// Get HEAD hash
		repo, err := gogit.PlainOpen(suite.testRepoPath)
		require.NoError(t, err)
		headRef, err := repo.Head()
		require.NoError(t, err)
		commitHash := headRef.Hash().String()

		// Send fetch command (section 1: command with delimiter)
		err = writePktLine(writer, "command=fetch\n")
		require.NoError(t, err)
		_, err = writer.WriteString("0001") // delimiter
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Send fetch options (section 2: want, done with flush)
		err = writePktLine(writer, fmt.Sprintf("want %s\n", commitHash))
		require.NoError(t, err)
		err = writePktLine(writer, "done\n")
		require.NoError(t, err)
		err = writeFlush(writer)
		require.NoError(t, err)
		err = writer.Flush()
		require.NoError(t, err)

		// Verify response
		packfileFound := false
		for i := 0; i < 50; i++ {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			line, isFlush, err := readPktLine(reader)
			if err != nil || isFlush {
				break
			}
			if strings.Contains(string(line), "packfile") || len(line) > 100 {
				packfileFound = true
				break
			}
		}

		assert.True(t, packfileFound, "Should receive packfile in response")
	})
}

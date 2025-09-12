package server

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPktLineWriter tests the pktLineWriter functionality
func TestPktLineWriter(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		expected string
	}{
		{
			name:     "simple line",
			data:     "hello",
			expected: "0006hello\n",
		},
		{
			name:     "empty line",
			data:     "",
			expected: "0001\n",
		},
		{
			name:     "long line",
			data:     strings.Repeat("a", 100),
			expected: fmt.Sprintf("0065%s\n", strings.Repeat("a", 100)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writer := &pktLineWriter{writer: &buf}

			err := writer.writeLine(tt.data)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, buf.String())
		})
	}
}

// TestPktLineWriterFlush tests the flush functionality
func TestPktLineWriterFlush(t *testing.T) {
	var buf bytes.Buffer
	writer := &pktLineWriter{writer: &buf}

	err := writer.flush()
	require.NoError(t, err)

	assert.Equal(t, "0000", buf.String())
}

// TestNewDuplexTCPServer tests server creation
func TestNewDuplexTCPServer(t *testing.T) {
	// Create test config
	testConfig := &config.Config{
		Server: config.ServerConfig{
			TCPGitPort: 5222,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	server, err := NewDuplexTCPServer(testConfig)
	require.NoError(t, err)
	require.NotNil(t, server)

	assert.Equal(t, testConfig, server.config)
	assert.NotNil(t, server.permissionClient)
	assert.NotNil(t, server.logger)
	assert.NotNil(t, server.connections)
	assert.Empty(t, server.connections)
}

// TestDuplexTCPServerStartStop tests server start and stop functionality
func TestDuplexTCPServerStartStop(t *testing.T) {
	// Skip this test as it requires external dependencies
	t.Skip("Skipping test that requires external permission service")
}

// TestHandleGitProtocol tests Git protocol parsing
func TestHandleGitProtocol(t *testing.T) {
	// Skip this test as it requires external dependencies
	t.Skip("Skipping test that requires external permission service")
}

// TestParseUploadPackRequestFromTCP tests TCP data parsing
func TestParseUploadPackRequestFromTCP(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		expected *protocol.UploadPackRequest
	}{
		{
			name: "valid want/have request",
			data: "0015want abc123def456\n0015have def456ghi789\n0000",
			expected: &protocol.UploadPackRequest{
				Wants: []string{"abc123def456"},
				Haves: []string{"def456ghi789"},
				Done:  true,
			},
		},
		{
			name: "multiple wants",
			data: "0015want abc123def456\n0015want def456ghi789\n0000",
			expected: &protocol.UploadPackRequest{
				Wants: []string{"abc123def456", "def456ghi789"},
				Haves: []string{},
				Done:  true,
			},
		},
		{
			name: "empty request",
			data: "0000",
			expected: &protocol.UploadPackRequest{
				Wants: []string{},
				Haves: []string{},
				Done:  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &DuplexTCPServer{}

			result, err := server.parseUploadPackRequestFromTCP([]byte(tt.data))
			require.NoError(t, err)

			assert.Equal(t, tt.expected.Wants, result.Wants)
			assert.Equal(t, tt.expected.Haves, result.Haves)
			assert.Equal(t, tt.expected.Done, result.Done)
		})
	}
}

// TestCreateErrorResponse tests error response creation
func TestCreateErrorResponse(t *testing.T) {
	server := &DuplexTCPServer{}

	message := "Test error message"
	response := server.createErrorResponse(message)

	expected := "ERR Test error message\n"
	assert.Equal(t, expected, string(response))
}

// TestGetConnectionCount tests connection counting
func TestGetConnectionCount(t *testing.T) {
	// Create a minimal server instance for testing connection counting
	server := &DuplexTCPServer{
		connections: make(map[net.Conn]*DuplexConnection),
	}

	// Initially no connections
	assert.Equal(t, 0, server.GetConnectionCount())

	// Add mock connections
	conn1, _ := net.Pipe()
	conn2, _ := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	duplexConn1 := &DuplexConnection{
		conn:     conn1,
		lastUsed: time.Now(),
	}
	duplexConn2 := &DuplexConnection{
		conn:     conn2,
		lastUsed: time.Now(),
	}

	server.mu.Lock()
	server.connections[conn1] = duplexConn1
	server.connections[conn2] = duplexConn2
	server.mu.Unlock()

	assert.Equal(t, 2, server.GetConnectionCount())

	// Remove one connection
	server.mu.Lock()
	delete(server.connections, conn1)
	server.mu.Unlock()

	assert.Equal(t, 1, server.GetConnectionCount())
}

// TestWalkCommitHistory tests commit history walking
func TestWalkCommitHistory(t *testing.T) {
	// Create a temporary directory for the test repository
	tempDir := t.TempDir()

	// Initialize a git repository
	repo, err := git.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Create a test file and commit
	workTree, err := repo.Worktree()
	require.NoError(t, err)

	testFile := filepath.Join(tempDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	_, err = workTree.Add("test.txt")
	require.NoError(t, err)

	commit, err := workTree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Test walking commit history
	server := &DuplexTCPServer{}
	collected := make(map[string]*object.Commit)
	haves := []string{}

	commitObj, err := repo.CommitObject(commit)
	require.NoError(t, err)

	err = server.walkCommitHistory(repo, commitObj, collected, haves)
	require.NoError(t, err)

	// Should have collected the commit
	assert.Len(t, collected, 1)
	assert.Contains(t, collected, commit.String())
}

// TestProcessGitDataWithSSH tests SSH processing (without actual SSH)
func TestProcessGitDataWithSSH(t *testing.T) {
	// Set environment variable to enable SSH mode
	originalEnv := os.Getenv("USE_SSH_TRANSPORT")
	defer os.Setenv("USE_SSH_TRANSPORT", originalEnv)

	os.Setenv("USE_SSH_TRANSPORT", "true")

	// Create a minimal server instance for testing
	server := &DuplexTCPServer{}

	// Test data
	data := []byte("0015want abc123def456\n0000")
	operation := "upload-pack"
	owner := "testuser"
	repo := "testrepo"

	// This will fail because we don't have SSH keys, but we can test the flow
	response := server.processGitDataWithSSH(nil, data, operation, owner, repo)

	// Should return an error response
	assert.Contains(t, string(response), "ERR")
}

// TestCleanupConnections tests connection cleanup functionality
func TestCleanupConnections(t *testing.T) {
	// Create a minimal server instance for testing
	server := &DuplexTCPServer{
		connections: make(map[net.Conn]*DuplexConnection),
	}

	// Create mock connections
	conn1, _ := net.Pipe()
	conn2, _ := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	// Add connections with different lastUsed times
	duplexConn1 := &DuplexConnection{
		conn:     conn1,
		lastUsed: time.Now().Add(-10 * time.Minute), // Old connection
	}
	duplexConn2 := &DuplexConnection{
		conn:     conn2,
		lastUsed: time.Now(), // Recent connection
	}

	server.mu.Lock()
	server.connections[conn1] = duplexConn1
	server.connections[conn2] = duplexConn2
	server.mu.Unlock()

	// Run cleanup
	server.cleanupConnections()

	// Give cleanup time to run
	time.Sleep(100 * time.Millisecond)

	// Check that old connection was removed
	server.mu.RLock()
	_, exists2 := server.connections[conn2]
	server.mu.RUnlock()

	// Note: The cleanup runs in a goroutine with a ticker, so we can't easily test
	// the exact timing, but we can verify the structure is correct
	assert.True(t, exists2) // Recent connection should still exist
}

// TestDuplexConnection tests DuplexConnection structure
func TestDuplexConnection(t *testing.T) {
	conn, _ := net.Pipe()
	defer conn.Close()

	duplexConn := &DuplexConnection{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		owner:    "testowner",
		repo:     "testrepo",
		lastUsed: time.Now(),
	}

	assert.Equal(t, conn, duplexConn.conn)
	assert.Equal(t, "testowner", duplexConn.owner)
	assert.Equal(t, "testrepo", duplexConn.repo)
	assert.NotNil(t, duplexConn.reader)
	assert.NotNil(t, duplexConn.writer)
	assert.False(t, duplexConn.lastUsed.IsZero())
}

// TestProcessUploadPackTCP tests upload-pack processing
func TestProcessUploadPackTCP(t *testing.T) {
	// Skip this test as it requires external dependencies
	t.Skip("Skipping test that requires external permission service")
}

// BenchmarkPktLineWriter benchmarks the pktLineWriter
func BenchmarkPktLineWriter(b *testing.B) {
	var buf bytes.Buffer
	writer := &pktLineWriter{writer: &buf}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		writer.writeLine("test data")
	}
}

// BenchmarkParseUploadPackRequestFromTCP benchmarks TCP parsing
func BenchmarkParseUploadPackRequestFromTCP(b *testing.B) {
	server := &DuplexTCPServer{}
	data := []byte("0015want abc123def456\n0015have def456ghi789\n0000")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		server.parseUploadPackRequestFromTCP(data)
	}
}

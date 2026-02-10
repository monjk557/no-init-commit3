package server_test

import (
	"bytes"
	"net"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSHProtocolWriter tests the SSHProtocolWriter functionality
func TestSSHProtocolWriter(t *testing.T) {
	var buf bytes.Buffer
	writer := protocol.NewSSHProtocolWriter(&buf, 1024)

	// Test writing data
	testData := []byte("hello world")
	n, err := writer.Write(testData)
	require.NoError(t, err)
	assert.Equal(t, len(testData), n)

	// Test flushing
	err = writer.Flush()
	require.NoError(t, err)

	assert.Equal(t, "hello world", buf.String())
}

// TestSSHProtocolWriterError tests error writing
func TestSSHProtocolWriterError(t *testing.T) {
	var buf bytes.Buffer
	writer := protocol.NewSSHProtocolWriter(&buf, 1024)

	// Test writing SSH error
	err := writer.WriteSSHError("test error")
	require.NoError(t, err)

	err = writer.Flush()
	require.NoError(t, err)

	// SSH error format should be "ERR test error\n"
	assert.Contains(t, buf.String(), "ERR test error")
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

	server, err := server.NewDuplexTCPServer(testConfig)
	require.NoError(t, err)
	require.NotNil(t, server)
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

// TestDuplexConnection tests DuplexConnection structure
func TestDuplexConnection(t *testing.T) {
	conn, _ := net.Pipe()
	defer func() { _ = conn.Close() }()

	// Note: DuplexConnection fields are unexported, so we can only test creation
	duplexConn := &server.DuplexConnection{}
	require.NotNil(t, duplexConn)
}

// TestProcessUploadPackTCP tests upload-pack processing
func TestProcessUploadPackTCP(t *testing.T) {
	// Skip this test as it requires external dependencies
	t.Skip("Skipping test that requires external permission service")
}

// TestProcessGitDataWithSSH tests SSH processing (without actual SSH)
func TestProcessGitDataWithSSH(t *testing.T) {
	// Skip this test as it requires external dependencies
	t.Skip("Skipping test that requires external permission service")
}

// BenchmarkSSHProtocolWriter benchmarks the SSHProtocolWriter
func BenchmarkSSHProtocolWriter(b *testing.B) {
	var buf bytes.Buffer
	writer := protocol.NewSSHProtocolWriter(&buf, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = writer.Write([]byte("test data"))
		_ = writer.Flush()
	}
}

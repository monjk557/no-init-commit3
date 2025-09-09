package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repository"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// pktLineWriter helps write Git protocol packet lines
type pktLineWriter struct {
	writer io.Writer
}

// writeLine writes a packet line in Git protocol format
func (p *pktLineWriter) writeLine(data string) error {
	// Calculate length: 4 bytes for length + data + 1 for newline
	length := len(data) + 1
	line := fmt.Sprintf("%04x%s\n", length, data)
	_, err := p.writer.Write([]byte(line))
	return err
}

// flush writes a flush packet (0000)
func (p *pktLineWriter) flush() error {
	_, err := p.writer.Write([]byte("0000"))
	return err
}

// DuplexTCPServer handles bidirectional TCP communication for Git operations
type DuplexTCPServer struct {
	config           *config.Config
	permissionClient permissions.PermissionClient
	listener         net.Listener
	connections      map[net.Conn]*DuplexConnection
	mu               sync.RWMutex
}

// DuplexConnection represents a bidirectional connection
type DuplexConnection struct {
	conn     net.Conn
	reader   *bufio.Reader
	writer   *bufio.Writer
	owner    string
	repo     string
	lastUsed time.Time
}

// NewDuplexTCPServer creates a new duplex TCP server
func NewDuplexTCPServer(config *config.Config) (*DuplexTCPServer, error) {
	// Create permission client
	permClient, err := permissions.NewPermissionClient(permissions.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create permission client: %w", err)
	}

	return &DuplexTCPServer{
		config:           config,
		permissionClient: permClient,
		connections:      make(map[net.Conn]*DuplexConnection),
	}, nil
}

// Start starts the TCP server on port 22
func (s *DuplexTCPServer) Start() error {
	listener, err := net.Listen("tcp", ":22")
	if err != nil {
		return fmt.Errorf("failed to listen on port 22: %w", err)
	}

	s.listener = listener
	fmt.Printf("🚀 Duplex TCP Server started on port 22\n")

	// Start connection cleanup goroutine
	go s.cleanupConnections()

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("❌ Failed to accept connection: %v\n", err)
			continue
		}

		fmt.Printf("🔌 New connection from: %s\n", conn.RemoteAddr())
		go s.handleConnection(conn)
	}
}

// Stop stops the TCP server
func (s *DuplexTCPServer) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// handleConnection handles a new TCP connection
func (s *DuplexTCPServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Create duplex connection
	duplexConn := &DuplexConnection{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		lastUsed: time.Now(),
	}

	// Register connection
	s.mu.Lock()
	s.connections[conn] = duplexConn
	s.mu.Unlock()

	// Cleanup on exit
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		fmt.Printf("🔌 Connection closed: %s\n", conn.RemoteAddr())
	}()

	// Handle Git protocol negotiation
	if err := s.handleGitProtocol(duplexConn); err != nil {
		fmt.Printf("❌ Git protocol error: %v\n", err)
		return
	}
}

// handleGitProtocol handles Git protocol negotiation and duplex communication
func (s *DuplexTCPServer) handleGitProtocol(conn *DuplexConnection) error {
	// Read initial Git command
	line, err := conn.reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read Git command: %w", err)
	}

	line = strings.TrimSpace(line)
	fmt.Printf("📨 Received Git command: %s\n", line)

	// Parse Git command (e.g., "git-upload-pack 'owner/repo.git'")
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return fmt.Errorf("invalid Git command format: %s", line)
	}

	command := parts[0]
	repoPath := strings.Trim(parts[1], "'")

	// Extract owner and repo from path
	pathParts := strings.Split(repoPath, "/")
	if len(pathParts) < 2 {
		return fmt.Errorf("invalid repository path: %s", repoPath)
	}

	owner := pathParts[0]
	repo := strings.TrimSuffix(pathParts[1], ".git")

	conn.owner = owner
	conn.repo = repo

	fmt.Printf("📁 Repository: %s/%s\n", owner, repo)

	// Handle different Git commands
	switch command {
	case "git-upload-pack":
		return s.handleUploadPack(conn)
	case "git-receive-pack":
		return s.handleReceivePack(conn)
	default:
		return fmt.Errorf("unsupported Git command: %s", command)
	}
}

// handleUploadPack handles git-upload-pack (fetch) operations
func (s *DuplexTCPServer) handleUploadPack(conn *DuplexConnection) error {
	fmt.Printf("⬇️  Handling git-upload-pack for %s/%s\n", conn.owner, conn.repo)

	// Send Git protocol advertisement
	advertisement := "000eversion 2\n" +
		"0010agent=git-executor\n" +
		"000fls-refs\n" +
		"000efetch\n" +
		"0000"

	if _, err := conn.writer.WriteString(advertisement); err != nil {
		return fmt.Errorf("failed to send advertisement: %w", err)
	}

	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush advertisement: %w", err)
	}

	// Start duplex communication
	return s.startDuplexCommunication(conn, "upload-pack")
}

// handleReceivePack handles git-receive-pack (push) operations
func (s *DuplexTCPServer) handleReceivePack(conn *DuplexConnection) error {
	fmt.Printf("⬆️  Handling git-receive-pack for %s/%s\n", conn.owner, conn.repo)

	// Send Git protocol advertisement
	advertisement := "000eversion 2\n" +
		"0010agent=git-executor\n" +
		"000freceive-pack\n" +
		"0000"

	if _, err := conn.writer.WriteString(advertisement); err != nil {
		return fmt.Errorf("failed to send advertisement: %w", err)
	}

	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush advertisement: %w", err)
	}

	// Start duplex communication
	return s.startDuplexCommunication(conn, "receive-pack")
}

// startDuplexCommunication starts bidirectional communication
func (s *DuplexTCPServer) startDuplexCommunication(conn *DuplexConnection, operation string) error {
	fmt.Printf("🔄 Starting duplex communication for %s\n", operation)

	// Create channels for bidirectional communication
	clientToServer := make(chan []byte, 100)
	serverToClient := make(chan []byte, 100)

	// Start goroutines for bidirectional data flow
	var wg sync.WaitGroup

	// Client to Server (read from client, send to processing)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(clientToServer)

		buffer := make([]byte, 4096)
		for {
			n, err := conn.reader.Read(buffer)
			if err != nil {
				if err != io.EOF {
					fmt.Printf("❌ Error reading from client: %v\n", err)
				}
				break
			}

			conn.lastUsed = time.Now()
			clientToServer <- buffer[:n]
		}
	}()

	// Server to Client (process data, send to client)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(serverToClient)

		for data := range clientToServer {
			// Process the data (here you would implement actual Git protocol logic)
			processedData := s.processGitData(data, operation, conn.owner, conn.repo)

			if len(processedData) > 0 {
				serverToClient <- processedData
			}
		}
	}()

	// Send processed data to client
	wg.Add(1)
	go func() {
		defer wg.Done()

		for data := range serverToClient {
			if _, err := conn.writer.Write(data); err != nil {
				fmt.Printf("❌ Error writing to client: %v\n", err)
				break
			}

			if err := conn.writer.Flush(); err != nil {
				fmt.Printf("❌ Error flushing to client: %v\n", err)
				break
			}
		}
	}()

	// Wait for all goroutines to complete
	wg.Wait()
	fmt.Printf("✅ Duplex communication completed for %s\n", operation)

	return nil
}

// processGitData processes incoming Git protocol data using upload-pack methods
func (s *DuplexTCPServer) processGitData(data []byte, operation, owner, repo string) []byte {
	fmt.Printf("📦 Processing %d bytes for %s/%s (%s)\n", len(data), owner, repo, operation)

	// Check if we should use SSH transport (you can add a flag or config option)
	useSSH := os.Getenv("USE_SSH_TRANSPORT") == "true"

	if useSSH {
		fmt.Printf("🔐 Using SSH transport for Git operations\n")
		return s.processGitDataWithSSH(data, operation, owner, repo)
	}

	// Use local filesystem access (existing method)
	uploadHandler, err := protocol.NewUploadPackHandler(s.config)
	if err != nil {
		fmt.Printf("❌ Failed to create upload-pack handler: %v\n", err)
		return s.createErrorResponse("Failed to create upload-pack handler")
	}

	// Route to appropriate handler based on operation
	switch operation {
	case "upload-pack":
		return s.processUploadPackTCP(data, owner, repo, uploadHandler)
	case "receive-pack":
		// For now, return error for receive-pack (push operations)
		return s.createErrorResponse("Receive-pack not implemented yet")
	default:
		return s.createErrorResponse("Unsupported operation: " + operation)
	}
}

// processUploadPackTCP processes upload-pack using existing upload-pack handler
func (s *DuplexTCPServer) processUploadPackTCP(data []byte, owner, repo string, uploadHandler *protocol.UploadPackHandler) []byte {
	// Create path config directly
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		fmt.Printf("❌ Failed to create path config: %v\n", err)
		return s.createErrorResponse("Failed to create path config")
	}

	// Get repository path
	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		fmt.Printf("❌ Invalid repository path: %v\n", err)
		return s.createErrorResponse("Invalid repository path")
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(pathConfig, repoPath); err != nil {
		fmt.Printf("❌ Repository not found: %v\n", err)
		return s.createErrorResponse("Repository not found")
	}

	// Open repository using go-git directly
	gogitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		fmt.Printf("❌ Failed to open repository: %v\n", err)
		return s.createErrorResponse("Failed to open repository")
	}

	// Parse the upload-pack request from TCP data
	uploadReq, err := s.parseUploadPackRequestFromTCP(data)
	if err != nil {
		fmt.Printf("❌ Failed to parse upload-pack request: %v\n", err)
		return s.createErrorResponse("Invalid upload-pack request")
	}

	// Create a buffer to capture the response
	var responseBuffer strings.Builder
	pktWriter := &pktLineWriter{writer: &responseBuffer}

	// Process want/have negotiation directly using Git filesystem access
	ctx := context.Background()
	err = s.processWantHaveNegotiationDirect(ctx, gogitRepo, uploadReq, pktWriter)
	if err != nil {
		fmt.Printf("❌ Failed to process want/have negotiation: %v\n", err)
		return s.createErrorResponse("Failed to process negotiation")
	}

	return []byte(responseBuffer.String())
}

// parseUploadPackRequestFromTCP parses upload-pack request from TCP data
func (s *DuplexTCPServer) parseUploadPackRequestFromTCP(data []byte) (*protocol.UploadPackRequest, error) {
	req := &protocol.UploadPackRequest{
		Wants: make([]string, 0),
		Haves: make([]string, 0),
	}

	// Parse pkt-line format from TCP data
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse pkt-line format (length + data)
		if len(line) >= 4 {
			lengthStr := line[:4]
			length, err := strconv.ParseInt(lengthStr, 16, 32)
			if err != nil {
				continue // Skip invalid lines
			}

			if length == 0 {
				req.Done = true
				break
			}

			if length > 4 {
				data := line[4:]
				if strings.HasPrefix(data, "want ") {
					hash := strings.TrimSpace(data[5:])
					req.Wants = append(req.Wants, hash)
				} else if strings.HasPrefix(data, "have ") {
					hash := strings.TrimSpace(data[5:])
					req.Haves = append(req.Haves, hash)
				}
			}
		}
	}

	return req, nil
}

// processWantHaveNegotiationDirect processes want/have negotiation directly using Git filesystem
func (s *DuplexTCPServer) processWantHaveNegotiationDirect(ctx context.Context, repo *gogit.Repository, req *protocol.UploadPackRequest, pktWriter *pktLineWriter) error {

	// Collect objects to send
	objectsToSend := make(map[string]*object.Commit)

	// Process each want request
	for _, wantHash := range req.Wants {
		hash := plumbing.NewHash(wantHash)
		if hash.IsZero() {
			continue
		}

		// Get commit object
		commit, err := repo.CommitObject(hash)
		if err != nil {
			fmt.Printf("⚠️  Commit not found: %s\n", wantHash)
			continue
		}

		// Walk commit history to collect all reachable commits
		err = s.walkCommitHistory(repo, commit, objectsToSend, req.Haves)
		if err != nil {
			return fmt.Errorf("failed to walk commit history: %w", err)
		}
	}

	// Send packfile header
	packfileHeader := fmt.Sprintf("# pack with %d objects\n", len(objectsToSend))
	if err := pktWriter.writeLine(packfileHeader); err != nil {
		return err
	}

	// Send object information
	for hash, commit := range objectsToSend {
		objectInfo := fmt.Sprintf("commit %s %s", hash, commit.Message)
		if err := pktWriter.writeLine(objectInfo); err != nil {
			return err
		}
	}

	// End with flush packet
	return pktWriter.flush()
}

// walkCommitHistory walks the commit history and collects commits
func (s *DuplexTCPServer) walkCommitHistory(repo *gogit.Repository, commit *object.Commit, collected map[string]*object.Commit, haves []string) error {
	hash := commit.Hash.String()

	// Check if we've already processed this commit
	if _, exists := collected[hash]; exists {
		return nil
	}

	// Check if this commit is in the "have" list (client already has it)
	for _, have := range haves {
		if hash == have {
			return nil // Don't include objects the client already has
		}
	}

	// Add this commit to our collection
	collected[hash] = commit

	// Recursively walk parent commits (limit depth to prevent infinite loops)
	if len(collected) < 1000 { // Safety limit
		parents := commit.Parents()
		for {
			parent, err := parents.Next()
			if err != nil {
				break // No more parents
			}
			if err := s.walkCommitHistory(repo, parent, collected, haves); err != nil {
				return err
			}
		}
	}

	return nil
}

// processGitDataWithSSH processes Git data using go-git SSH transport
func (s *DuplexTCPServer) processGitDataWithSSH(data []byte, operation, owner, repo string) []byte {
	fmt.Printf("🔐 Processing %d bytes for %s/%s (%s) using SSH transport\n", len(data), owner, repo, operation)

	// Create SSH authentication
	sshAuth, err := s.createSSHAuth()
	if err != nil {
		fmt.Printf("❌ Failed to create SSH auth: %v\n", err)
		return s.createErrorResponse("Failed to create SSH authentication")
	}

	// Create remote repository URL
	remoteURL := fmt.Sprintf("git@github.com:%s/%s.git", owner, repo)

	// Create temporary directory for cloning
	tempDir := fmt.Sprintf("/tmp/git-temp-%s-%s-%d", owner, repo, time.Now().UnixNano())
	defer func() {
		// Clean up temporary directory
		os.RemoveAll(tempDir)
	}()

	// Clone repository using SSH
	gitRepo, err := gogit.PlainClone(tempDir, false, &gogit.CloneOptions{
		URL:      remoteURL,
		Auth:     sshAuth,
		Progress: os.Stdout,
	})
	if err != nil {
		fmt.Printf("❌ Failed to clone repository via SSH: %v\n", err)
		return s.createErrorResponse("Failed to clone repository via SSH")
	}

	// Process the operation based on type
	switch operation {
	case "upload-pack":
		return s.processUploadPackWithSSH(gitRepo, data, owner, repo)
	case "receive-pack":
		return s.processReceivePackWithSSH(gitRepo, data, owner, repo)
	default:
		return s.createErrorResponse("Unsupported operation: " + operation)
	}
}

// createSSHAuth creates SSH authentication for go-git
func (s *DuplexTCPServer) createSSHAuth() (transport.AuthMethod, error) {
	// Try to use SSH key from default location
	sshKeyPath := os.Getenv("HOME") + "/.ssh/id_rsa"

	// Check if key exists
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		// Try alternative key locations
		alternativePaths := []string{
			os.Getenv("HOME") + "/.ssh/id_ed25519",
			os.Getenv("HOME") + "/.ssh/id_ecdsa",
			"/etc/ssh/ssh_host_rsa_key",
		}

		for _, path := range alternativePaths {
			if _, err := os.Stat(path); err == nil {
				sshKeyPath = path
				break
			}
		}
	}

	// Read SSH private key
	sshKey, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Parse private key
	signer, err := gossh.ParsePrivateKey(sshKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	// Create SSH public key authentication
	auth := &ssh.PublicKeys{
		User:   "git",
		Signer: signer,
	}

	return auth, nil
}

// processUploadPackWithSSH processes upload-pack using SSH transport
func (s *DuplexTCPServer) processUploadPackWithSSH(gitRepo *gogit.Repository, data []byte, owner, repoName string) []byte {
	// Get repository references
	refs, err := gitRepo.References()
	if err != nil {
		fmt.Printf("❌ Failed to get references: %v\n", err)
		return s.createErrorResponse("Failed to get repository references")
	}

	// Create response buffer
	var responseBuffer strings.Builder
	pktWriter := &pktLineWriter{writer: &responseBuffer}

	// Write Git protocol advertisement
	advertisement := "000eversion 2\n" +
		"0010agent=git-executor-ssh\n" +
		"000fls-refs\n" +
		"000efetch\n" +
		"0000"

	responseBuffer.WriteString(advertisement)

	// List references
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			refLine := fmt.Sprintf("%s %s\n", ref.Hash().String(), ref.Name().String())
			return pktWriter.writeLine(refLine)
		}
		return nil
	})

	if err != nil {
		fmt.Printf("❌ Failed to list references: %v\n", err)
		return s.createErrorResponse("Failed to list references")
	}

	// End with flush packet
	pktWriter.flush()

	return []byte(responseBuffer.String())
}

// processReceivePackWithSSH processes receive-pack using SSH transport
func (s *DuplexTCPServer) processReceivePackWithSSH(gitRepo *gogit.Repository, data []byte, owner, repoName string) []byte {
	// For receive-pack, we would need to parse the incoming pack data
	// and apply it to the repository
	// This is more complex and would require implementing the full receive-pack protocol

	fmt.Printf("📤 Processing receive-pack via SSH for %s/%s\n", owner, repoName)

	// For now, return a simple acknowledgment
	response := "000eversion 2\n" +
		"0010agent=git-executor-ssh\n" +
		"000freceive-pack\n" +
		"0000"

	return []byte(response)
}

// createErrorResponse creates a Git protocol error response
func (s *DuplexTCPServer) createErrorResponse(message string) []byte {
	// Git protocol error format: ERR <message>\n
	errorMsg := fmt.Sprintf("ERR %s\n", message)
	return []byte(errorMsg)
}

// cleanupConnections cleans up idle connections
func (s *DuplexTCPServer) cleanupConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for conn, duplexConn := range s.connections {
			if now.Sub(duplexConn.lastUsed) > 5*time.Minute {
				fmt.Printf("🧹 Cleaning up idle connection: %s\n", conn.RemoteAddr())
				conn.Close()
				delete(s.connections, conn)
			}
		}
		s.mu.Unlock()
	}
}

// GetConnectionCount returns the number of active connections
func (s *DuplexTCPServer) GetConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

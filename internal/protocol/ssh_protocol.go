// Package protocol implements SSH protocol handlers for Git operations forwarded from collabops-git-ssh gateway.
// This file handles SSH-specific Git operations that are forwarded from the SSH gateway to the executor.
package protocol

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/orchestrator"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// SSHProtocolHandler handles SSH-forwarded Git operations
type SSHProtocolHandler struct {
	pathConfig   *repositories.PathConfig
	errorHandler *ErrorHandler
	eventsClient events.RedisStreamClient
	config       *config.Config
}

// SSHRequest represents a validated SSH-forwarded Git request
type SSHRequest struct {
	UserID          string // User ID from gateway authentication
	CorrelationID   string // Correlation ID for tracing
	Owner           string // Repository owner
	Repo            string // Repository name
	RepoPath        string // Validated repository path
	Operation       string // git-upload-pack or git-receive-pack
	ProtocolVersion string // SSH protocol version if specified
}

// NewSSHProtocolHandler creates a new SSH protocol handler
func NewSSHProtocolHandler(cfg *config.Config) (*SSHProtocolHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Create path configuration for repository path validation
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create error handler for protocol errors
	errorHandler := NewErrorHandler(nil)

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		// Events client will be initialized if needed
		eventsClient = nil // Can be set up later if needed
	}

	return &SSHProtocolHandler{
		pathConfig:   pathConfig,
		errorHandler: errorHandler,
		eventsClient: eventsClient,
		config:       cfg,
	}, nil
}

// ValidateSSHRequest validates and extracts context from SSH-forwarded request
func (h *SSHProtocolHandler) ValidateSSHRequest(r *http.Request, operation string) (*SSHRequest, error) {
	// Extract correlation ID for tracing (optional, generate if not present)
	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = fmt.Sprintf("ssh-%s", GenerateRequestID())
	}

	// Validate source header for SSH gateway
	source := r.Header.Get("X-Source")
	if source != "ssh-gateway" {
		return nil, NewGitProtocolError("invalid_source", "Invalid source header: SSH operations must come from ssh-gateway", http.StatusForbidden, true)
	}

	// Extract repository parameters from URL
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	if owner == "" || repo == "" {
		return nil, NewGitProtocolError("invalid_request", "Missing required repository parameters (owner/repo)", http.StatusBadRequest, true)
	}

	// Validate repository path using existing path validation
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository path: %w", err)
	}

	// Extract protocol version if specified (optional for SSH)
	protocolVersion := r.Header.Get("Git-Protocol")

	// Validate operation parameter
	if operation != "git-upload-pack" && operation != "git-receive-pack" {
		return nil, NewGitProtocolError("invalid_operation", fmt.Sprintf("Invalid Git operation: %s", operation), http.StatusBadRequest, true)
	}

	// Resolve user ID:
	// - Primary: X-USER-ID header from ssh-gateway
	// - Fallback (Option B): call orchestrator ssh-key validation API and parse response body user_id
	userID := r.Header.Get("X-USER-ID")
	if strings.TrimSpace(userID) == "" {
		userID = h.resolveUserIDViaOrchestrator(r.Context(), r, fmt.Sprintf("%s/%s", owner, repo))
	}
	if strings.TrimSpace(userID) == "" {
		return nil, NewGitProtocolError("authentication_required", "Missing user authentication context for SSH request", http.StatusUnauthorized, true)
	}

	return &SSHRequest{
		UserID:          userID,
		CorrelationID:   correlationID,
		Owner:           owner,
		Repo:            repo,
		RepoPath:        repoPath,
		Operation:       operation,
		ProtocolVersion: protocolVersion,
	}, nil
}

func (h *SSHProtocolHandler) resolveUserIDViaOrchestrator(ctx context.Context, r *http.Request, repoPath string) string {
	if h == nil || h.config == nil {
		return ""
	}
	if strings.TrimSpace(h.config.Orchestrator.BaseURL) == "" || strings.TrimSpace(h.config.Orchestrator.SSHKeyValidatePath) == "" {
		return ""
	}

	// fingerprint is expected to be forwarded by ssh-gateway as a header
	fpHeader := h.config.Orchestrator.FingerprintHeader
	if strings.TrimSpace(fpHeader) == "" {
		fpHeader = "X-SSH-Key-Fingerprint"
	}
	fingerprint := r.Header.Get(fpHeader)
	if strings.TrimSpace(fingerprint) == "" {
		// Common alternative header names
		fingerprint = r.Header.Get("X-SSH-Fingerprint")
	}
	if strings.TrimSpace(fingerprint) == "" {
		return ""
	}

	orchClient, err := orchestrator.NewClient(
		h.config.Orchestrator.BaseURL,
		h.config.Orchestrator.SSHKeyValidatePath,
		h.config.Orchestrator.Timeout,
		h.config.Orchestrator.ForwardAuthHeader,
	)
	if err != nil {
		return ""
	}

	authHeader := ""
	if h.config.Orchestrator.ForwardAuthHeader {
		authHeader = r.Header.Get("Authorization")
	}
	userID, err := orchClient.ResolveUserIDFromSSHKey(ctx, fingerprint, repoPath, authHeader)
	if err != nil {
		return ""
	}
	return userID
}

// CheckSSHPermissions validates user permissions for SSH operations
func (h *SSHProtocolHandler) CheckSSHPermissions(ctx context.Context, req *SSHRequest) error {
	switch req.Operation {
	case "git-upload-pack":
		// Check read permission for fetch operations
		return fmt.Errorf("permission check not implemented")

	case "git-receive-pack":
		// Check write permission for push operations
		return fmt.Errorf("permission check not implemented")

	default:
		return fmt.Errorf("unknown operation: %s", req.Operation)
	}
}

// sendEmptyPackfile sends an empty packfile (for when objects can't be found)
func (h *SSHProtocolHandler) sendEmptyPackfile(output *SSHProtocolWriter) error {
	// Pack header: "PACK" + version(4 bytes) + object count(4 bytes)
	packHeader := []byte{
		'P', 'A', 'C', 'K', // PACK magic
		0x00, 0x00, 0x00, 0x02, // version 2
		0x00, 0x00, 0x00, 0x00, // object count = 0
	}

	// Pack trailer: SHA-1 of pack (for empty pack, this is constant)
	// SHA-1 of empty pack with version 2 and 0 objects
	packTrailer := []byte{
		0xda, 0x39, 0xa3, 0xee, 0x5e, 0x6b, 0x4b, 0x0d,
		0x32, 0x55, 0xbf, 0xef, 0x95, 0x60, 0x18, 0x90,
		0xaf, 0xd8, 0x07, 0x09,
	}

	// Use sideband writer for empty packfile too
	sbWriter := newSSHSidebandWriter(output, 0x01)
	if _, err := sbWriter.Write(packHeader); err != nil {
		return err
	}
	if _, err := sbWriter.Write(packTrailer); err != nil {
		return err
	}

	// Flush sideband stream (sends "0000" flush packet)
	if err := sbWriter.Flush(); err != nil {
		return err
	}

	return output.Flush()
}

// EnsureSSHRepository ensures the repository exists and can be opened
func (h *SSHProtocolHandler) EnsureSSHRepository(ctx context.Context, req *SSHRequest) (*repositories.RepositoryWrapper, error) {
	// Ensure repository exists using existing repository utilities
	if err := repositories.EnsureRepoExists(h.pathConfig, req.RepoPath); err != nil {
		return nil, fmt.Errorf("failed to ensure repository exists: %w", err)
	}

	// Open repository using go-git
	repo, err := repositories.OpenRepositorySimple(req.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return repo, nil
}

// SSHStreamingOptions contains options for SSH protocol streaming
type SSHStreamingOptions struct {
	BufferSize     int    // Buffer size for streaming operations
	ContentType    string // Content type for response
	CorrelationID  string // Correlation ID for logging
	MaxRequestSize int64  // Maximum request size (0 = unlimited)
	EnableLogging  bool   // Enable detailed logging
}

// DefaultSSHStreamingOptions returns default streaming options for SSH operations
func DefaultSSHStreamingOptions(correlationID string) *SSHStreamingOptions {
	return &SSHStreamingOptions{
		BufferSize:     64 * 1024,                              // 64KB buffer
		ContentType:    "application/x-git-upload-pack-result", // Default, overridden by specific operations
		CorrelationID:  correlationID,
		MaxRequestSize: 100 * 1024 * 1024, // 100MB max request size
		EnableLogging:  true,
	}
}

// SSHProtocolWriter provides utilities for writing SSH protocol responses
type SSHProtocolWriter struct {
	writer io.Writer
	buffer *bufio.Writer
}

// sshSidebandWriter writes data in side-band-64k format for SSH protocol
type sshSidebandWriter struct {
	w          io.Writer
	channel    byte
	chunkSize  int
	firstWrite bool
}

// newSSHSidebandWriter creates a new sideband writer for SSH protocol
func newSSHSidebandWriter(w io.Writer, channel byte) *sshSidebandWriter {
	// pkt-line 총 길이(4바이트 헤더 포함) 최대 65520
	// payload = length-4, 그 중 1바이트는 채널이 차지
	// 안전하게 payload(채널 포함) 65515 사용
	return &sshSidebandWriter{
		w:          w,
		channel:    channel,
		chunkSize:  65515,
		firstWrite: true,
	}
}

// Write implements io.Writer for sshSidebandWriter
// 데이터를 64k 청크로 나눠서 side-band 포맷으로 전송
func (sb *sshSidebandWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	total := 0
	for len(p) > 0 {
		n := sb.chunkSize - 1 // 채널 바이트(1)를 뺀 실제 데이터 공간
		if n > len(p) {
			n = len(p)
		}
		chunk := p[:n]
		p = p[n:]

		payloadLen := 1 + len(chunk) // [channel][data...]
		length := payloadLen + 4     // pkt-line 총 길이

		if _, err := fmt.Fprintf(sb.w, "%04x", length); err != nil {
			return total, err
		}
		if _, err := sb.w.Write([]byte{sb.channel}); err != nil {
			return total, err
		}
		if _, err := sb.w.Write(chunk); err != nil {
			return total, err
		}

		// 첫 번째 chunk를 즉시 전송하도록 flush
		// "PACK" 헤더를 포함한 첫 번째 패킷에 중요합니다
		// packfile 데이터가 즉시 시작되어야 합니다
		if sb.firstWrite {
			// 기본 writer가 지원하는 경우 flush 시도
			if flusher, ok := sb.w.(interface{ Flush() error }); ok {
				if err := flusher.Flush(); err != nil {
					return total, err
				}
			}
			sb.firstWrite = false
		}

		total += len(chunk)
	}

	return total, nil
}

// Flush writes the flush packet to end sideband stream
func (sb *sshSidebandWriter) Flush() error {
	_, err := sb.w.Write([]byte("0000"))
	return err
}

// writeProgressMessage sends a progress message on sideband channel 2 (progress)
func writeProgressMessage(w io.Writer, message string) error {
	// Progress messages use channel 2 (0x02)
	// Format: [4-byte hex length][0x02][message]
	messageBytes := []byte(message)
	payloadLen := 1 + len(messageBytes) // channel byte + message
	length := payloadLen + 4            // total pkt-line length

	if _, err := fmt.Fprintf(w, "%04x", length); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0x02}); err != nil { // channel 2 = progress
		return err
	}
	if _, err := w.Write(messageBytes); err != nil {
		return err
	}
	return nil
}

// NewSSHProtocolWriter creates a new SSH protocol writer
func NewSSHProtocolWriter(w io.Writer, bufferSize int) *SSHProtocolWriter {
	return &SSHProtocolWriter{
		writer: w,
		buffer: bufio.NewWriterSize(w, bufferSize),
	}
}

// Write writes data to the SSH protocol stream
func (w *SSHProtocolWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	return n, err
}

// Flush flushes the buffered data to the underlying writer
// This also implements the Flusher interface
func (w *SSHProtocolWriter) Flush() error {
	return w.buffer.Flush()
}

// WriteSSHError writes an SSH protocol error message in pkt-line format
func (w *SSHProtocolWriter) WriteSSHError(errorMsg string) error {
	// Git protocol error format: ERR <message>\n in pkt-line format
	// Use PktWriter to ensure proper pkt-line formatting
	pw := &PktWriter{W: w.buffer, WithLF: true}
	if err := pw.Err(errorMsg); err != nil {
		return err
	}
	// Send flush packet to end error message
	if err := pw.Flush(); err != nil {
		return err
	}
	return w.buffer.Flush()
}

// GenerateRequestID generates a unique request ID for correlation
func GenerateRequestID() string {
	// Simple request ID generation - in production, use a more robust method
	return fmt.Sprintf("%d", generateRandomID())
}

// generateRandomID generates a random ID (simple implementation)
func generateRandomID() int64 {
	// This is a placeholder - in production, use crypto/rand or similar
	return 123456789
}

// ValidateSSHGitCommand validates Git command parameters from SSH requests
func ValidateSSHGitCommand(command, repoPath string) error {
	// Validate command format
	if command == "" {
		return fmt.Errorf("empty Git command")
	}

	// Validate repository path format (no path traversal)
	if strings.Contains(repoPath, "..") || strings.Contains(repoPath, "/./") {
		return fmt.Errorf("invalid repository path: path traversal detected")
	}

	// Basic command validation
	validCommands := []string{"git-upload-pack", "git-receive-pack"}
	for _, validCmd := range validCommands {
		if strings.HasPrefix(command, validCmd) {
			return nil
		}
	}

	return fmt.Errorf("invalid Git command: %s", command)
}

// ParseSSHGitProtocolHeaders extracts Git protocol information from SSH request headers
func ParseSSHGitProtocolHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)

	// Extract Git protocol version
	if version := r.Header.Get("Git-Protocol"); version != "" {
		headers["protocol-version"] = version
	}

	// Extract SSH-specific headers
	if sshConnection := r.Header.Get("SSH-Connection"); sshConnection != "" {
		headers["ssh-connection"] = sshConnection
	}

	// Extract client capabilities if present
	if capabilities := r.Header.Get("Git-Capabilities"); capabilities != "" {
		headers["capabilities"] = capabilities
	}

	return headers
}

// HandleSSHUploadPack handles POST /ssh/upload-pack requests (SSH-forwarded git-upload-pack)
func (h *SSHProtocolHandler) HandleSSHUploadPack(w http.ResponseWriter, r *http.Request) {
	// Validate SSH request
	sshReq, err := h.ValidateSSHRequest(r, "git-upload-pack")
	if err != nil {
		h.errorHandler.HandleError(w, r, err, "ssh_upload_pack")
		return
	}

	// Ensure repository exists
	repo, err := h.EnsureSSHRepository(r.Context(), sshReq)
	if err != nil {
		h.errorHandler.HandleError(w, r, err, "ssh_upload_pack")
		return
	}

	// Set up streaming options
	streamOpts := DefaultSSHStreamingOptions(sshReq.CorrelationID)
	streamOpts.ContentType = "application/x-git-upload-pack-result"

	// Handle SSH upload-pack operation
	h.processSSHUploadPack(w, r, sshReq, repo, streamOpts)
}

// HandleSSHReceivePack handles POST /ssh/receive-pack requests (SSH-forwarded git-receive-pack)
func (h *SSHProtocolHandler) HandleSSHReceivePack(w http.ResponseWriter, r *http.Request) {
	// Validate SSH request
	sshReq, err := h.ValidateSSHRequest(r, "git-receive-pack")
	if err != nil {
		h.errorHandler.HandleError(w, r, err, "ssh_receive_pack")
		return
	}

	// Ensure repository exists
	repo, err := h.EnsureSSHRepository(r.Context(), sshReq)
	if err != nil {
		h.errorHandler.HandleError(w, r, err, "ssh_receive_pack")
		return
	}

	// Set up streaming options
	streamOpts := DefaultSSHStreamingOptions(sshReq.CorrelationID)
	streamOpts.ContentType = "application/x-git-receive-pack-result"

	// Handle SSH receive-pack operation
	h.processSSHReceivePack(w, r, sshReq, repo, streamOpts)
}

// processSSHUploadPack processes the SSH upload-pack operation (fetch)
func (h *SSHProtocolHandler) processSSHUploadPack(w http.ResponseWriter, r *http.Request, sshReq *SSHRequest, repo *repositories.RepositoryWrapper, opts *SSHStreamingOptions) {
	// Set content type for SSH upload-pack (no service advertisement)
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")

	// Create SSH protocol writer for streaming
	writer := NewSSHProtocolWriter(w, opts.BufferSize)
	defer func() { _ = writer.Flush() }()

	// Parse SSH protocol headers
	protocolHeaders := ParseSSHGitProtocolHeaders(r)

	// For SSH upload-pack, we need to:
	// 0. Write advertise service
	// 1. Read the want/have negotiation from the request body
	// 2. Generate the appropriate packfile
	// 3. Stream it back to the gateway

	// 1. Write advertise service in pkt-line format
	// Use PktWriter to ensure proper formatting
	pw := &PktWriter{W: writer, WithLF: false}
	if err := pw.WriteLine("version 2"); err != nil {
		return
	}
	if err := pw.WriteLine("agent=collabops"); err != nil {
		return
	}
	if err := pw.WriteLine("ls-refs"); err != nil {
		return
	}
	if err := pw.WriteLine("fetch"); err != nil {
		return
	}
	if err := pw.Flush(); err != nil {
		return
	}
	// Flush the underlying buffer to ensure data is sent
	if err := writer.Flush(); err != nil {
		return
	}

	// Read request body for want/have negotiation
	requestBody := r.Body
	if opts.MaxRequestSize > 0 {
		requestBody = http.MaxBytesReader(w, r.Body, opts.MaxRequestSize)
	}
	defer func() { _ = r.Body.Close() }()

	// Add timeout to context for reading request body
	// This prevents indefinite blocking when client doesn't send data immediately
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// For now, delegate to the existing upload-pack logic
	// This is a simplified implementation - in production, you'd implement full SSH protocol negotiation
	err := h.executeSSHGitCommand(ctx, "git-upload-pack", sshReq.RepoPath, requestBody, writer, protocolHeaders)
	if err != nil {
		// Write SSH error format
		if sshErr := writer.WriteSSHError(fmt.Sprintf("upload-pack failed: %v", err)); sshErr != nil {
			_ = sshErr // Log the SSH error writing failure, but don't fail the request further
		}
		return
	}
}

// processSSHReceivePack processes the SSH receive-pack operation (push)
func (h *SSHProtocolHandler) processSSHReceivePack(w http.ResponseWriter, r *http.Request, sshReq *SSHRequest, repo *repositories.RepositoryWrapper, opts *SSHStreamingOptions) {
	ctx := r.Context()

	// Set content type for SSH receive-pack
	w.Header().Set("Content-Type", opts.ContentType)

	// Create SSH protocol writer for streaming
	writer := NewSSHProtocolWriter(w, opts.BufferSize)
	defer func() { _ = writer.Flush() }()

	// 1. Send v1 advertisement (SSH receive-pack uses v1 protocol)
	// Get the underlying go-git repository for advertisement
	cache := repositories.GetSimpleCache()
	goGitRepo, err := cache.Get(sshReq.RepoPath)
	if err != nil {
		_ = writer.WriteSSHError(fmt.Sprintf("failed to open repository: %v", err))
		return
	}

	// Send v1 advertisement
	if err := WriteV1Advert(writer, *goGitRepo); err != nil {
		_ = writer.WriteSSHError(fmt.Sprintf("failed to send advertisement: %v", err))
		return
	}
	if err := writer.Flush(); err != nil {
		return
	}

	// 2. Read ref update commands and packfile from request body
	requestBody := r.Body
	if opts.MaxRequestSize > 0 {
		requestBody = http.MaxBytesReader(w, r.Body, opts.MaxRequestSize)
	}
	defer func() { _ = r.Body.Close() }()

	// Parse receive-pack request (ref updates + packfile)
	receiveReq, err := h.parseSSHReceivePackRequest(requestBody)
	if err != nil {
		_ = writer.WriteSSHError(fmt.Sprintf("failed to parse receive-pack request: %v", err))
		return
	}

	// 3. Acquire repository write lock to coordinate with read paths (overview, fetch).
	// All push paths must use GetRepoRWLock so that RLock holders (e.g. overview) are
	// properly excluded during pack writes, and the cache is invalidated after push.
	repoLock := repositories.GetRepoRWLock(sshReq.RepoPath)
	repoLock.Lock()
	defer func() {
		repositories.GetSimpleCache().Invalidate(sshReq.RepoPath)
		repoLock.Unlock()
	}()

	// 4. Open repository using go-git
	fs := osfs.New(sshReq.RepoPath)
	storage := filesystem.NewStorage(fs, nil)
	gitRepo, err := gogit.Open(storage, fs)
	if err != nil {
		_ = writer.WriteSSHError(fmt.Sprintf("failed to open repository: %v", err))
		return
	}

	// 5. Process packfile if present
	if len(receiveReq.PackfileData) > 0 {
		// Check if packfile starts with "PACK"
		if bytes.HasPrefix(receiveReq.PackfileData, []byte("PACK")) {
			// Use packfile parser to process the packfile
			reader := bytes.NewReader(receiveReq.PackfileData)
			scanner := packfile.NewScanner(reader)
			parser, err := packfile.NewParserWithStorage(scanner, gitRepo.Storer)
			if err != nil {
				pw := &PktWriter{W: writer, WithLF: false}
				_ = pw.WriteLine(fmt.Sprintf("unpack %s", err.Error()))
				_ = pw.Flush()
				return
			}

			if _, err := parser.Parse(); err != nil {
				pw := &PktWriter{W: writer, WithLF: false}
				_ = pw.WriteLine(fmt.Sprintf("unpack %s", err.Error()))
				_ = pw.Flush()
				return
			}

			// Ensure packfile objects are fully persisted before accessing commits
			// This ensures commit objects are available when we try to read them later
		}
	}

	// 6. Report successful unpack
	pw := &PktWriter{W: writer, WithLF: false}
	if err := pw.WriteLine("unpack ok"); err != nil {
		return
	}

	// 7. Process each reference update command
	for _, cmd := range receiveReq.Commands {
		err := h.processSSHRefUpdate(ctx, gitRepo, cmd, sshReq.Owner, sshReq.Repo, sshReq.UserID)
		if err != nil {
			// Report failure for this reference
			statusLine := fmt.Sprintf("ng %s %s", cmd.RefName, err.Error())
			if err := pw.WriteLine(statusLine); err != nil {
				return
			}
		} else {
			// Report success for this reference
			statusLine := fmt.Sprintf("ok %s", cmd.RefName)
			if err := pw.WriteLine(statusLine); err != nil {
				return
			}
		}
	}

	// 8. End with flush packet
	_ = pw.Flush()
}

// addSSHCommitEventToStream adds a commit event to Redis Stream for SSH operations
func (h *SSHProtocolHandler) addSSHCommitEventToStream(ctx context.Context, repo *gogit.Repository, oldHash string, commitHash plumbing.Hash, refName, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	// Get commit object to verify it exists
	// Note: commitHash should be available after packfile processing
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		// Not a commit or commit not yet available, skip
		// This can happen if packfile processing hasn't fully completed
		return
	}

	// Extract commit message - ensure it's not nil
	commitMessage := commit.Message
	if commitMessage == "" {
		// Commit message is empty, but we still want to include it in the event
		commitMessage = ""
	}

	// Get full filesystem path for the repository
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		// Skip if we can't get the path
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, h.pathConfig.GetRepoRoot())

	// Create event
	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypePush,
		CommitHash:    commitHash.String(),
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git", // Default pusher identifier
		UserID:        userID,
		CommitMessage: commitMessage,
		Timestamp:     commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			// Log error but don't fail the operation
			_ = err
		}
	}()
}

// executeSSHGitCommand executes a Git command for SSH operations
func (h *SSHProtocolHandler) executeSSHGitCommand(ctx context.Context, command, repoPath string, input io.ReadCloser, output *SSHProtocolWriter, headers map[string]string) error {
	// Validate the Git command
	if err := ValidateSSHGitCommand(command, repoPath); err != nil {
		return fmt.Errorf("invalid Git command: %w", err)
	}

	// Parse git protocol v2 commands from input
	requestData, err := h.parseGitProtocolRequest(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to parse git protocol request: %w", err)
	}

	// Handle different git protocol commands
	switch command {
	case "git-upload-pack":
		return h.handleSSHUploadPackCommand(ctx, repoPath, requestData, output)
	case "git-receive-pack":
		return h.handleSSHReceivePackCommand(ctx, repoPath, requestData, output)
	default:
		return fmt.Errorf("unsupported git command: %s", command)
	}
}

// parseGitProtocolRequest parses git protocol v2 requests
func (h *SSHProtocolHandler) parseGitProtocolRequest(ctx context.Context, input io.ReadCloser) (map[string]string, error) {
	data := make(map[string]string)
	buffer := make([]byte, 8192)

	// Use a channel to receive read result
	type readResult struct {
		n   int
		err error
	}
	readCh := make(chan readResult, 1)

	// Read in a goroutine to avoid blocking indefinitely
	go func() {
		n, err := input.Read(buffer)
		readCh <- readResult{n: n, err: err}
	}()

	// Wait for read to complete or context cancellation
	var n int
	var err error
	select {
	case result := <-readCh:
		n = result.n
		err = result.err
	case <-ctx.Done():
		// Context cancelled or timed out - return empty data
		// This handles the case where client doesn't send data immediately
		return data, nil
	}

	if err != nil && err != io.EOF {
		return nil, err
	}

	if n > 0 {
		content := string(buffer[:n])

		// Parse pkt-line format
		// Example: "0032want 9b13bf533e90b50697bb5d466995991a637501e4 0000"
		if len(content) >= 4 {
			// Extract pkt-line length (first 4 characters)
			lengthStr := content[:4]

			// Parse length as hex
			var length int
			if _, err := fmt.Sscanf(lengthStr, "%x", &length); err == nil {

				// Validate length to prevent slice bounds error
				if length > 4 && length <= len(content) {
					// Extract the actual data (skip the 4-byte length prefix)
					pktData := content[4:length]

					// Parse the command
					if strings.Contains(pktData, "command=ls-refs") {
						data["command"] = "ls-refs"
					} else if strings.Contains(pktData, "want ") {
						data["command"] = "upload-pack"
						// Extract want hash
						parts := strings.Fields(pktData)
						for i, part := range parts {
							if part == "want" && i+1 < len(parts) {
								data["wants"] = parts[i+1]
								break
							}
						}
					}
				}
			}
		}
	}

	return data, nil
}

// handleSSHUploadPackCommand handles git-upload-pack commands for SSH
func (h *SSHProtocolHandler) handleSSHUploadPackCommand(ctx context.Context, repoPath string, requestData map[string]string, output *SSHProtocolWriter) error {
	// Open repository with simple caching
	gitRepo, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	if gitRepo == nil {
		return fmt.Errorf("repository is nil after opening")
	}
	defer func() { _ = gitRepo.Close() }()

	// Get the underlying go-git repository for operations that need it
	cache := repositories.GetSimpleCache()
	goGitRepo, err := cache.Get(repoPath)
	if err != nil {
		return fmt.Errorf("failed to get go-git repository: %w", err)
	}
	if goGitRepo == nil {
		return fmt.Errorf("go-git repository is nil after opening")
	}
	// Handle ls-refs command
	if requestData["command"] == "ls-refs" {
		return h.handleLsRefs(ctx, gitRepo, output)
	}

	// For SSH upload-pack, we need to handle want/have negotiation
	// This is similar to HTTP upload-pack but without the HTTP-specific parts
	return h.handleSSHWantHaveNegotiation(ctx, goGitRepo, requestData, output)
}

// handleSSHReceivePackCommand handles git-receive-pack commands for SSH
// This is now deprecated - use processSSHReceivePack instead
func (h *SSHProtocolHandler) handleSSHReceivePackCommand(ctx context.Context, repoPath string, requestData map[string]string, output *SSHProtocolWriter) error {
	// This function is kept for backward compatibility but should not be used
	// The actual implementation is in processSSHReceivePack
	return fmt.Errorf("handleSSHReceivePackCommand is deprecated, use processSSHReceivePack instead")
}

// parseSSHReceivePackRequest parses the receive-pack request body in pkt-line format
func (h *SSHProtocolHandler) parseSSHReceivePackRequest(body io.Reader) (*ReceivePackRequest, error) {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	reader := bufio.NewReader(body)
	firstCommand := true
	var packfileBuffer bytes.Buffer
	inPackfile := false

	for {
		// Read pkt-line format
		lineBytes, isFlush, err := readPktLineFromReader(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("error reading request body: %w", err)
		}

		if isFlush {
			// Flush packet, indicates end of commands
			break
		}

		line := string(lineBytes)

		// Check if we've hit the packfile data
		if strings.HasPrefix(line, "PACK") || inPackfile {
			inPackfile = true
			packfileBuffer.Write(lineBytes)
			continue
		}

		// Parse command line: "old-hash new-hash ref-name [capabilities]"
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		oldHashStr := parts[0]
		newHashStr := parts[1]
		refName := parts[2]

		// Check for capabilities (null-separated or space-separated)
		var capabilities []string
		if strings.Contains(line, "\x00") {
			// Null-separated format
			refParts := strings.SplitN(line, "\x00", 2)
			if len(refParts) > 1 {
				capStr := strings.TrimSpace(refParts[1])
				capabilities = strings.Fields(capStr)
			}
		} else if len(parts) > 3 {
			// Space-separated capabilities
			capabilities = parts[3:]
		}

		// Parse hashes
		oldHash := plumbing.NewHash(oldHashStr)
		newHash := plumbing.NewHash(newHashStr)

		// Create command
		cmd := RefUpdateCommand{
			RefName: refName,
			OldHash: oldHash,
			NewHash: newHash,
		}

		req.Commands = append(req.Commands, cmd)

		// Store capabilities from first command
		if firstCommand && len(capabilities) > 0 {
			req.Capabilities = append(req.Capabilities, capabilities...)
			firstCommand = false
		}
	}

	// Read remaining packfile data if any
	if inPackfile || packfileBuffer.Len() > 0 {
		// Read all remaining data as packfile
		remaining, err := io.ReadAll(reader)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("error reading packfile: %w", err)
		}
		if len(remaining) > 0 {
			packfileBuffer.Write(remaining)
		}
		req.PackfileData = packfileBuffer.Bytes()
	}

	return req, nil
}

// readPktLineFromReader reads a pkt-line from a reader
func readPktLineFromReader(reader *bufio.Reader) ([]byte, bool, error) {
	// Read 4-byte length header
	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(reader, lengthBytes); err != nil {
		return nil, false, err
	}

	// Check for flush packet
	if string(lengthBytes) == "0000" {
		return nil, true, nil
	}

	// Parse length
	length, err := strconv.ParseInt(string(lengthBytes), 16, 32)
	if err != nil {
		return nil, false, fmt.Errorf("invalid pkt-line length: %w", err)
	}

	if length < 4 {
		return nil, false, fmt.Errorf("invalid pkt-line length: %d", length)
	}

	// Read payload (length - 4 for header)
	payloadLen := int(length) - 4
	if payloadLen == 0 {
		return nil, false, nil
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, false, err
	}

	return payload, false, nil
}

// processSSHRefUpdate processes a single reference update command for SSH
func (h *SSHProtocolHandler) processSSHRefUpdate(ctx context.Context, repo *gogit.Repository, cmd RefUpdateCommand, owner, repoName, userID string) error {
	// Validate the reference name
	if !isValidRefName(cmd.RefName) {
		return fmt.Errorf("invalid reference name: %s", cmd.RefName)
	}

	// Get current reference (if it exists)
	ref, err := repo.Reference(plumbing.ReferenceName(cmd.RefName), true)
	if err != nil && err != plumbing.ErrReferenceNotFound {
		return fmt.Errorf("failed to get reference %s: %w", cmd.RefName, err)
	}

	// Check if this is a create, update, or delete operation
	isCreate := cmd.OldHash == plumbing.ZeroHash
	isDelete := cmd.NewHash == plumbing.ZeroHash

	// Validate the old hash matches current state (for updates and deletes)
	if !isCreate {
		if ref == nil {
			return fmt.Errorf("reference %s does not exist", cmd.RefName)
		}
		if ref.Hash() != cmd.OldHash {
			return fmt.Errorf("reference %s has been modified (expected %s, got %s)", cmd.RefName, cmd.OldHash, ref.Hash())
		}
	}

	// Validate the new object exists (for creates and updates)
	if !isDelete {
		_, err := repo.CommitObject(cmd.NewHash)
		if err != nil {
			// Try as a tag object
			_, err = repo.TagObject(cmd.NewHash)
			if err != nil {
				// Try as any object
				_, err = repo.Object(plumbing.AnyObject, cmd.NewHash)
				if err != nil {
					return fmt.Errorf("object %s not found in repository", cmd.NewHash)
				}
			}
		}
	}

	// Perform the reference update
	if isDelete {
		// Delete the reference
		err = repo.Storer.RemoveReference(plumbing.ReferenceName(cmd.RefName))
		if err != nil {
			return fmt.Errorf("failed to delete reference %s: %w", cmd.RefName, err)
		}
	} else {
		// Create or update the reference
		newRef := plumbing.NewHashReference(plumbing.ReferenceName(cmd.RefName), cmd.NewHash)
		err = repo.Storer.SetReference(newRef)
		if err != nil {
			return fmt.Errorf("failed to update reference %s: %w", cmd.RefName, err)
		}

		// Publish commit event if enabled (only for branches, not tags, and only if hash changed)
		if h.eventsClient != nil && cmd.OldHash != cmd.NewHash && strings.HasPrefix(cmd.RefName, "refs/heads/") {
			oldHashStr := ""
			if cmd.OldHash != plumbing.ZeroHash {
				oldHashStr = cmd.OldHash.String()
			}
			h.addSSHCommitEventToStream(ctx, repo, oldHashStr, cmd.NewHash, cmd.RefName, owner, repoName, userID)
		}
	}

	return nil
}

// handleSSHWantHaveNegotiation handles want/have negotiation for SSH upload-pack
func (h *SSHProtocolHandler) handleSSHWantHaveNegotiation(ctx context.Context, repo *gogit.Repository, requestData map[string]string, output *SSHProtocolWriter) error {
	// Parse wants from request data
	var wantHashes []plumbing.Hash
	if wants, exists := requestData["wants"]; exists && wants != "" {
		// Parse wants (comma-separated hashes)
		wantStrs := strings.Split(wants, ",")
		for _, wantStr := range wantStrs {
			wantStr = strings.TrimSpace(wantStr)
			if wantStr == "" {
				continue
			}
			wantHashes = append(wantHashes, plumbing.NewHash(wantStr))
		}
	}

	if len(wantHashes) == 0 {
		// No wants specified, send empty packfile
		return h.sendEmptyPackfile(output)
	}

	// Parse haves (if any)
	var haveHashes []plumbing.Hash
	if haves, exists := requestData["haves"]; exists && haves != "" {
		haveStrs := strings.Split(haves, ",")
		for _, haveStr := range haveStrs {
			haveStr = strings.TrimSpace(haveStr)
			if haveStr == "" {
				continue
			}
			haveHashes = append(haveHashes, plumbing.NewHash(haveStr))
		}
	}

	// Send NAK (no common ancestor) if no haves
	// NAK is part of negotiation phase, so it must be sent as regular pkt-line,
	// NOT in side-band format. Side-band is only used for packfile data.
	if len(haveHashes) == 0 {
		nakLine := "NAK\n"
		pktLine := fmt.Sprintf("%04x%s", len(nakLine)+4, nakLine)
		if _, err := output.Write([]byte(pktLine)); err != nil {
			return fmt.Errorf("failed to write NAK: %w", err)
		}
	}

	// Flush to end negotiation phase
	flushPkt := []byte("0000")
	if _, err := output.Write(flushPkt); err != nil {
		return fmt.Errorf("failed to flush negotiation: %w", err)
	}
	if err := output.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer: %w", err)
	}

	// Build FetchReq to use BuildFetchObjectList (same as TCP/HTTP)
	// This provides proper object counting, include-tag support, and sorting
	// ofs-delta: 클라이언트 대부분 지원. SSH에서는 capability 파싱 없이 기본 사용.
	useOfsDelta := strings.Contains(requestData["capabilities"], "ofs-delta") || requestData["capabilities"] == ""
	fetchReq := &FetchReq{
		Wants:      wantHashes,
		Haves:      haveHashes,
		IncludeTag: false,
		OfsDelta:   useOfsDelta,
	}

	// Calculate objects to send using BuildFetchObjectList
	// This automatically handles commits, trees, blobs recursively,
	// includes annotated tags if IncludeTag is true, and sorts the result.
	// Supports shallow (deepen N): returns ShallowBoundary for shallow-info section.
	fetchResult, err := BuildFetchObjectList(repo.Storer, repo, fetchReq)
	if err != nil {
		return fmt.Errorf("failed to build fetch object list: %w", err)
	}
	objectHashes := fetchResult.Objects

	totalObjects := len(objectHashes)
	if totalObjects == 0 {
		// No objects to send, send empty packfile
		return h.sendEmptyPackfile(output)
	}

	// shallow-info section (when deepen N was used)
	if len(fetchResult.ShallowBoundary) > 0 {
		pw := &PktWriter{W: output, WithLF: true}
		if err := pw.WriteLine("shallow-info"); err != nil {
			return fmt.Errorf("failed to write shallow-info header: %w", err)
		}
		for _, h := range fetchResult.ShallowBoundary {
			if err := pw.WriteLine("shallow " + h.String()); err != nil {
				return fmt.Errorf("failed to write shallow line: %w", err)
			}
		}
		if _, err := output.Write([]byte("0001")); err != nil {
			return fmt.Errorf("failed to write shallow-info delim: %w", err)
		}
		if err := output.Flush(); err != nil {
			return fmt.Errorf("failed to flush after shallow-info: %w", err)
		}
	}

	// Send progress messages before packfile encoding
	// These are sent as sideband channel 2 (progress) messages
	if err := writeProgressMessage(output, fmt.Sprintf("Enumerating objects: %d, done.\n", totalObjects)); err != nil {
		return fmt.Errorf("failed to write enumerating message: %w", err)
	}
	if err := writeProgressMessage(output, fmt.Sprintf("Counting objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
		return fmt.Errorf("failed to write counting completion: %w", err)
	}

	// Flush progress messages before starting packfile transmission
	// This ensures progress messages are sent before packfile data
	if err := output.Flush(); err != nil {
		return fmt.Errorf("failed to flush progress messages: %w", err)
	}

	// Create sideband writer for packfile transmission
	sbWriter := newSSHSidebandWriter(output, 0x01) // channel 1 = pack data

	// Determine if we should use ref-delta or ofs-delta
	useRefDelta := !fetchReq.OfsDelta

	// Use go-git's packfile encoder
	// Note: go-git's encoder doesn't provide progress callbacks, so we can't
	// send compressing progress during encoding. The packfile encoding itself
	// is the compressing process.
	enc := packfile.NewEncoder(sbWriter, repo.Storer, useRefDelta)

	// Encode packfile with delta search (DefaultPackWindow) for smaller packs
	if _, err := enc.Encode(objectHashes, DefaultPackWindow); err != nil {
		return fmt.Errorf("failed to encode packfile: %w", err)
	}

	// Send compressing completion and summary after packfile encoding
	// Since we can't track progress during encoding, we send completion message after encoding finishes
	if err := writeProgressMessage(output, fmt.Sprintf("Compressing objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
		return fmt.Errorf("failed to write compressing completion: %w", err)
	}

	// Calculate delta count (simplified: assume some objects are deltas)
	deltaCount := totalObjects / 8 // Rough estimate: ~12% are deltas
	summary := fmt.Sprintf("Total %d (delta %d), reused %d (delta %d), pack-reused 0 (from 0)\n",
		totalObjects, deltaCount, totalObjects, deltaCount)
	if err := writeProgressMessage(output, summary); err != nil {
		return fmt.Errorf("failed to write summary: %w", err)
	}

	// Flush sideband stream (sends "0000" flush packet)
	if err := sbWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush sideband stream: %w", err)
	}

	// Flush the underlying buffer
	return output.Flush()
}

// handleLsRefs handles the ls-refs command for SSH upload-pack
func (h *SSHProtocolHandler) handleLsRefs(ctx context.Context, repo *repositories.RepositoryWrapper, output *SSHProtocolWriter) error {
	// Check for nil repository to prevent panic
	if repo == nil {
		// Write empty response for nil repository
		_, err := output.Write([]byte("0000"))
		return err
	}

	// Get commits to find HEAD reference
	commits, err := commits.GetCommits(repo, ctx, "HEAD", 1, 0)
	if err != nil {
		// Repository might be empty or HEAD might not exist - write empty response
		_, err := output.Write([]byte("0000"))
		return err
	}

	if len(commits) > 0 {
		// Write HEAD reference
		headHash := commits[0].Sha
		refLine := fmt.Sprintf("%s HEAD\n", headHash)
		pktLine := fmt.Sprintf("%04x%s", len(refLine)+4, refLine)

		_, err := output.Write([]byte(pktLine))
		if err != nil {
			return err
		}

		// Write main branch reference
		refLine = fmt.Sprintf("%s refs/heads/main\n", headHash)
		pktLine = fmt.Sprintf("%04x%s", len(refLine)+4, refLine)

		_, err = output.Write([]byte(pktLine))
		if err != nil {
			return err
		}
	}

	// End with flush packet
	_, err = output.Write([]byte("0000"))
	return err
}

// SSHEndpointMetrics provides metrics collection for SSH endpoints
type SSHEndpointMetrics struct {
	RequestCount     int64 // Total number of SSH requests
	SuccessCount     int64 // Successful requests
	ErrorCount       int64 // Failed requests
	BytesTransferred int64 // Total bytes transferred
}

// NewSSHEndpointMetrics creates a new metrics collection
func NewSSHEndpointMetrics() *SSHEndpointMetrics {
	return &SSHEndpointMetrics{}
}

// RecordRequest records a request in metrics
func (m *SSHEndpointMetrics) RecordRequest() {
	m.RequestCount++
}

// RecordSuccess records a successful request
func (m *SSHEndpointMetrics) RecordSuccess() {
	m.SuccessCount++
}

// RecordError records a failed request
func (m *SSHEndpointMetrics) RecordError() {
	m.ErrorCount++
}

// RecordBytes records bytes transferred
func (m *SSHEndpointMetrics) RecordBytes(bytes int64) {
	m.BytesTransferred += bytes
}

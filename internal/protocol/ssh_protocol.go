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

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/orchestrator"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
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
	caps := []string{"report-status", "delete-refs", "thin-pack", "atomic"}
	if err := WriteV1Advert(writer, sshReq.RepoPath, caps...); err != nil {
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

	// 4. Process packfile if present using native git index-pack
	if len(receiveReq.PackfileData) > 0 {
		if bytes.HasPrefix(receiveReq.PackfileData, []byte("PACK")) {
			packReader := bytes.NewReader(receiveReq.PackfileData)
			indexCtx, indexCancel := context.WithTimeout(ctx, 10*time.Minute)
			_, indexErr := gitcmd.IndexPackFromReader(indexCtx, sshReq.RepoPath, packReader)
			indexCancel()
			if indexErr != nil {
				pw := &PktWriter{W: writer, WithLF: false}
				_ = pw.WriteLine(fmt.Sprintf("unpack %s", indexErr.Error()))
				// V1 report-status requires ng lines for each ref; without them
				// the client reports "remote failed to report status".
				for _, cmd := range receiveReq.Commands {
					_ = pw.WriteLine(fmt.Sprintf("ng %s unpack failed", cmd.RefName))
				}
				_ = pw.Flush()
				return
			}
		}
	}

	// 6. Report successful unpack
	pw := &PktWriter{W: writer, WithLF: false}
	if err := pw.WriteLine("unpack ok"); err != nil {
		return
	}

	// 7. Process reference updates atomically via native git update-ref
	if len(receiveReq.Commands) > 0 {
		var updates []gitcmd.RefUpdateCmd
		for _, cmd := range receiveReq.Commands {
			updates = append(updates, gitcmd.RefUpdateCmd{
				RefName: cmd.RefName,
				OldOID:  cmd.OldHash,
				NewOID:  cmd.NewHash,
			})
		}
		atomicErr := gitcmd.AtomicRefUpdate(ctx, sshReq.RepoPath, updates)
		for _, cmd := range receiveReq.Commands {
			if atomicErr != nil {
				statusLine := fmt.Sprintf("ng %s %s", cmd.RefName, atomicErr.Error())
				if err := pw.WriteLine(statusLine); err != nil {
					return
				}
			} else {
				statusLine := fmt.Sprintf("ok %s", cmd.RefName)
				if err := pw.WriteLine(statusLine); err != nil {
					return
				}
				// Publish commit events for successful branch updates
				if h.eventsClient != nil && cmd.OldHash != cmd.NewHash && strings.HasPrefix(cmd.RefName, "refs/heads/") {
					oldHashStr := ""
					if cmd.OldHash != zeroHash {
						oldHashStr = cmd.OldHash
					}
					h.addSSHCommitEventToStream(ctx, sshReq.RepoPath, oldHashStr, cmd.NewHash, cmd.RefName, sshReq.Owner, sshReq.Repo, sshReq.UserID)
				}
			}
		}
	}

	// 8. End with flush packet
	_ = pw.Flush()
}

// addSSHCommitEventToStream adds a commit event to Redis Stream for SSH operations using native git.
func (h *SSHProtocolHandler) addSSHCommitEventToStream(ctx context.Context, repoPath, oldHash, commitHash, refName, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	// Get commit info using native git cat-file
	commitInfo, err := gitcmd.GetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, h.pathConfig.GetRepoRoot())

	// Create event
	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypePush,
		CommitHash:    commitHash,
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: commitInfo.Message,
		Timestamp:     commitInfo.CommitterDate,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(pubCtx, event); err != nil {
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
	// Handle ls-refs command
	if requestData["command"] == "ls-refs" {
		return h.handleLsRefs(ctx, repoPath, output)
	}

	// For SSH upload-pack, handle want/have negotiation using native git
	return h.handleSSHWantHaveNegotiation(ctx, repoPath, requestData, output)
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

		// Create command with string hashes
		cmd := RefUpdateCommand{
			RefName: refName,
			OldHash: oldHashStr,
			NewHash: newHashStr,
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

// processSSHRefUpdate is no longer used — ref updates are handled atomically
// in processSSHReceivePack via gitcmd.AtomicRefUpdate.

// handleSSHWantHaveNegotiation handles want/have negotiation for SSH upload-pack
// using native git pack-objects.
func (h *SSHProtocolHandler) handleSSHWantHaveNegotiation(ctx context.Context, repoPath string, requestData map[string]string, output *SSHProtocolWriter) error {
	// Parse wants from request data
	var wants []string
	if wantStr, exists := requestData["wants"]; exists && wantStr != "" {
		for _, w := range strings.Split(wantStr, ",") {
			w = strings.TrimSpace(w)
			if w != "" {
				wants = append(wants, w)
			}
		}
	}

	if len(wants) == 0 {
		return h.sendEmptyPackfile(output)
	}

	// Parse haves (if any)
	var haves []string
	if haveStr, exists := requestData["haves"]; exists && haveStr != "" {
		for _, h := range strings.Split(haveStr, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				haves = append(haves, h)
			}
		}
	}

	// Send NAK (no common ancestor) if no haves
	if len(haves) == 0 {
		nakLine := "NAK\n"
		pktLine := fmt.Sprintf("%04x%s", len(nakLine)+4, nakLine)
		if _, err := output.Write([]byte(pktLine)); err != nil {
			return fmt.Errorf("failed to write NAK: %w", err)
		}
	}

	// Flush to end negotiation phase
	if _, err := output.Write([]byte("0000")); err != nil {
		return fmt.Errorf("failed to flush negotiation: %w", err)
	}
	if err := output.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer: %w", err)
	}

	// Use native git pack-objects for packfile generation
	useOfsDelta := strings.Contains(requestData["capabilities"], "ofs-delta") || requestData["capabilities"] == ""

	opts := &gitcmd.PackObjectsOpts{
		Wants:    wants,
		Haves:    haves,
		OfsDelta: useOfsDelta,
	}

	// Create sideband writer for packfile transmission
	sbWriter := newSSHSidebandWriter(output, 0x01)

	_, err := gitcmd.RunPackObjects(ctx, repoPath, opts, sbWriter)
	if err != nil {
		return fmt.Errorf("git pack-objects: %w", err)
	}

	// Flush sideband stream
	if err := sbWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush sideband stream: %w", err)
	}

	return output.Flush()
}

// handleLsRefs handles the ls-refs command for SSH upload-pack using native git.
func (h *SSHProtocolHandler) handleLsRefs(ctx context.Context, repoPath string, output *SSHProtocolWriter) error {
	// Resolve HEAD
	headInfo, err := gitcmd.ResolveHEAD(ctx, repoPath)
	if err == nil && headInfo.OID != "" {
		refLine := fmt.Sprintf("%s HEAD\n", headInfo.OID)
		pktLine := fmt.Sprintf("%04x%s", len(refLine)+4, refLine)
		if _, err := output.Write([]byte(pktLine)); err != nil {
			return err
		}
	}

	// List refs
	refs, err := gitcmd.ListRefs(ctx, repoPath, "refs/heads/", "refs/tags/")
	if err == nil {
		for _, ref := range refs {
			refLine := fmt.Sprintf("%s %s\n", ref.ObjectName, ref.RefName)
			pktLine := fmt.Sprintf("%04x%s", len(refLine)+4, refLine)
			if _, err := output.Write([]byte(pktLine)); err != nil {
				return err
			}
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

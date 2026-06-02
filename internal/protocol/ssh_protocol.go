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
	"github.com/collabops/git-executor/internal/performance"
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

// processSSHUploadPack processes the SSH upload-pack operation (fetch).
// Implements Git protocol v2 over SSH: advertisement → ls-refs → fetch → packfile.
// Reuses the same pkt-line parsing, negotiation, and pack-objects logic as the HTTP handler.
func (h *SSHProtocolHandler) processSSHUploadPack(w http.ResponseWriter, r *http.Request, sshReq *SSHRequest, _ *repositories.RepositoryWrapper, opts *SSHStreamingOptions) {
	ctx := r.Context()
	repoPath := sshReq.RepoPath

	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")

	writer := NewSSHProtocolWriter(w, opts.BufferSize)
	defer func() { _ = writer.Flush() }()

	// 1. Send Git protocol v2 advertisement (same as HTTP info/refs)
	if err := WriteV2Advert(writer, "git/collabops-git-executor", "ls-refs", "fetch=shallow filter"); err != nil {
		return
	}
	if err := writer.Flush(); err != nil {
		return
	}

	// Set up request body reader
	var body io.Reader = r.Body
	if opts.MaxRequestSize > 0 {
		body = http.MaxBytesReader(w, r.Body, opts.MaxRequestSize)
	}
	defer func() { _ = r.Body.Close() }()

	pktWriter := performance.NewOptimizedPktLineWriter(writer)

	// 2. Read and handle command sections in a loop.
	// SSH protocol v2 flow: client sends ls-refs first, then fetch.
	for {
		cmds, err := readPktLineSection(body)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}
		if len(cmds) == 0 {
			return
		}

		if strings.HasPrefix(cmds[0], "command=ls-refs") {
			// Read ls-refs options (second section after delimiter)
			lsRefsOpts, err := readPktLineSection(body)
			if err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				return
			}
			allCmds := append(cmds, lsRefsOpts...)
			lsRefsReq, err := ParseLsRefsSection(allCmds)
			if err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				return
			}

			if err := handleLsRefsNative(ctx, repoPath, lsRefsReq, pktWriter); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				return
			}
			_ = pktWriter.Flush()
			_ = writer.Flush()
			continue // read next command (fetch)

		} else if strings.HasPrefix(cmds[0], "command=fetch") {
			// Read fetch options (second section after delimiter)
			fetchOpts, err := readPktLineSection(body)
			if err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				return
			}
			allCmds := append(cmds, fetchOpts...)
			fetchReq, err := ParseFetchSection(allCmds)
			if err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				return
			}

			if err := ValidateFetchSupport(fetchReq); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.Flush()
				return
			}

			// 3. Fetch negotiation: acknowledgments + ready (same as HTTP handler)
			// Filter haves to only those that exist on the server.
			// Clients may send OIDs that were force-pushed away or GC'd on the server;
			// passing unknown OIDs to pack-objects causes "fatal: bad object".
			var knownHaves []string
			if !fetchReq.Done {
				if err := pktWriter.WriteLine("acknowledgments"); err != nil {
					return
				}
				for _, have := range fetchReq.Haves {
					if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
						if err := pktWriter.WriteLine(fmt.Sprintf("ACK %s", have)); err != nil {
							return
						}
						knownHaves = append(knownHaves, have)
					}
				}
				if len(knownHaves) == 0 {
					if err := pktWriter.WriteLine("NAK"); err != nil {
						return
					}
				}
				if err := pktWriter.WriteLine("ready"); err != nil {
					return
				}
				// Delimiter (0001) — NOT flush (0000)
				if err := pktWriter.WriteDelim(); err != nil {
					return
				}
			} else {
				// Client sent "done" — still filter haves for pack-objects
				for _, have := range fetchReq.Haves {
					if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
						knownHaves = append(knownHaves, have)
					}
				}
			}

			// 4. Shallow-info section (if applicable)
			var shallowGrafts []string
			if fetchReq.Deepen > 0 {
				shallowGrafts, err = GetShallowBoundary(repoPath, fetchReq)
				if err != nil {
					_ = pktWriter.WriteLine(fmt.Sprintf("ERR shallow boundary: %s", err.Error()))
					_ = pktWriter.Flush()
					return
				}
			}
			if len(shallowGrafts) > 0 {
				if err := pktWriter.WriteLine("shallow-info"); err != nil {
					return
				}
				for _, sh := range shallowGrafts {
					if err := pktWriter.WriteLine("shallow " + sh); err != nil {
						return
					}
				}
				if err := pktWriter.WriteDelim(); err != nil {
					return
				}
			}

			// 5. Packfile section header
			if err := pktWriter.WriteLine("packfile"); err != nil {
				return
			}

			// 6. Stream packfile via native git pack-objects with sideband encoding
			packShallow := append(fetchReq.Shallow, shallowGrafts...)
			sbWriter := newSidebandWriter(writer, 0x01)
			packOpts := &gitcmd.PackObjectsOpts{
				Wants:         fetchReq.Wants,
				Haves:         knownHaves,
				Shallow:       packShallow,
				ThinPack:      fetchReq.ThinPack,
				OfsDelta:      fetchReq.OfsDelta,
				IncludeTag:    fetchReq.IncludeTag,
				DeepenSince:   fetchReq.DeepenSince,
				DeepenExclude: fetchReq.DeepenNot,
				Filter:        fetchReq.Filter,
			}
			if _, err := gitcmd.RunPackObjects(ctx, repoPath, packOpts, sbWriter); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = writer.Flush()
				return
			}
			if err := sbWriter.Flush(); err != nil {
				_ = writer.Flush()
				return
			}
			_ = writer.Flush()
			return // fetch completes the session

		} else {
			_ = pktWriter.WriteLine("ERR unknown command")
			return
		}
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

	// 4. Process packfile if present using native git index-pack — streamed, not buffered.
	if receiveReq.PackfileReader != nil {
		indexCtx, indexCancel := context.WithTimeout(ctx, 10*time.Minute)
		_, indexErr := gitcmd.IndexPackFromReader(indexCtx, sshReq.RepoPath, receiveReq.PackfileReader)
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
		PushedAt:      time.Now(),
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

	// Stream packfile data: combine any already-buffered prefix with the remaining reader.
	if inPackfile || packfileBuffer.Len() > 0 {
		req.PackfileReader = io.MultiReader(&packfileBuffer, reader)
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

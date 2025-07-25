// Package protocol implements SSH protocol handlers for Git operations forwarded from collabops-git-ssh gateway.
// This file handles SSH-specific Git operations that are forwarded from the SSH gateway to the executor.
package protocol

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-chi/chi/v5"
)

// SSHProtocolHandler handles SSH-forwarded Git operations
type SSHProtocolHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
	errorHandler     *ErrorHandler
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
func NewSSHProtocolHandler(cfg *config.Config, permissionClient permissions.PermissionClient) (*SSHProtocolHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if permissionClient == nil || (permissionClient != nil && isNilInterface(permissionClient)) {
		return nil, fmt.Errorf("permission client cannot be nil")
	}

	// Create path configuration for repository path validation
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create error handler for protocol errors
	errorHandler := NewErrorHandler(nil)

	return &SSHProtocolHandler{
		pathConfig:       pathConfig,
		permissionClient: permissionClient,
		errorHandler:     errorHandler,
	}, nil
}

// ValidateSSHRequest validates and extracts context from SSH-forwarded request
func (h *SSHProtocolHandler) ValidateSSHRequest(r *http.Request, operation string) (*SSHRequest, error) {
	// Extract user ID from gateway authentication header
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, NewGitProtocolError("authentication_required", "Missing user authentication context from SSH gateway", http.StatusUnauthorized, true)
	}

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
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")

	if owner == "" || repo == "" {
		return nil, NewGitProtocolError("invalid_request", "Missing required repository parameters (owner/repo)", http.StatusBadRequest, true)
	}

	// Validate repository path using existing path validation
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, WrapRepositoryError(err, fmt.Sprintf("%s/%s", owner, repo))
	}

	// Extract protocol version if specified (optional for SSH)
	protocolVersion := r.Header.Get("Git-Protocol")

	// Validate operation parameter
	if operation != "git-upload-pack" && operation != "git-receive-pack" {
		return nil, NewGitProtocolError("invalid_operation", fmt.Sprintf("Invalid Git operation: %s", operation), http.StatusBadRequest, true)
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

// CheckSSHPermissions validates user permissions for SSH operations
func (h *SSHProtocolHandler) CheckSSHPermissions(ctx context.Context, req *SSHRequest) error {
	switch req.Operation {
	case "git-upload-pack":
		// Check read permission for fetch operations
		hasPermission, err := h.permissionClient.CheckReadAccess(ctx, req.UserID, req.Owner, req.Repo)
		if err != nil {
			return WrapPermissionError(err, "check_read_access")
		}
		if !hasPermission {
			return NewGitProtocolError("access_denied", "Access denied to repository", http.StatusForbidden, true)
		}

	case "git-receive-pack":
		// Check write permission for push operations
		hasPermission, err := h.permissionClient.CheckWriteAccess(ctx, req.UserID, req.Owner, req.Repo)
		if err != nil {
			return WrapPermissionError(err, "check_write_access")
		}
		if !hasPermission {
			return NewGitProtocolError("write_access_denied", "Write access denied to repository", http.StatusForbidden, true)
		}

	default:
		return NewGitProtocolError("unknown_operation", fmt.Sprintf("Unknown operation: %s", req.Operation), http.StatusBadRequest, true)
	}

	return nil
}

// EnsureSSHRepository ensures the repository exists and can be opened
func (h *SSHProtocolHandler) EnsureSSHRepository(ctx context.Context, req *SSHRequest) (git.Repository, error) {
	// Ensure repository exists using existing repository utilities
	if err := repository.EnsureRepoExists(h.pathConfig, req.RepoPath); err != nil {
		return nil, WrapRepositoryError(err, req.RepoPath)
	}

	// Open repository using go-git
	repo, err := git.OpenRepository(req.RepoPath)
	if err != nil {
		return nil, WrapRepositoryError(err, req.RepoPath)
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
	return w.buffer.Write(p)
}

// Flush flushes the buffered data to the underlying writer
func (w *SSHProtocolWriter) Flush() error {
	return w.buffer.Flush()
}

// WriteSSHError writes an SSH protocol error message
func (w *SSHProtocolWriter) WriteSSHError(errorMsg string) error {
	// SSH protocol error format: ERR <message>\n
	_, err := fmt.Fprintf(w.buffer, "ERR %s\n", errorMsg)
	if err != nil {
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

	// Check permissions
	if err := h.CheckSSHPermissions(r.Context(), sshReq); err != nil {
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

	// Check permissions
	if err := h.CheckSSHPermissions(r.Context(), sshReq); err != nil {
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
func (h *SSHProtocolHandler) processSSHUploadPack(w http.ResponseWriter, r *http.Request, sshReq *SSHRequest, repo git.Repository, opts *SSHStreamingOptions) {
	// Set content type for SSH upload-pack
	w.Header().Set("Content-Type", opts.ContentType)

	// Create SSH protocol writer for streaming
	writer := NewSSHProtocolWriter(w, opts.BufferSize)
	defer func() { _ = writer.Flush() }()

	// Parse SSH protocol headers
	protocolHeaders := ParseSSHGitProtocolHeaders(r)

	// For SSH upload-pack, we need to:
	// 1. Read the want/have negotiation from the request body
	// 2. Generate the appropriate packfile
	// 3. Stream it back to the gateway

	// Read request body for want/have negotiation
	requestBody := r.Body
	if opts.MaxRequestSize > 0 {
		requestBody = http.MaxBytesReader(w, r.Body, opts.MaxRequestSize)
	}
	defer func() { _ = r.Body.Close() }()

	// For now, delegate to the existing upload-pack logic
	// This is a simplified implementation - in production, you'd implement full SSH protocol negotiation
	err := h.executeSSHGitCommand(r.Context(), "git-upload-pack", sshReq.RepoPath, requestBody, writer, protocolHeaders)
	if err != nil {
		// Write SSH error format
		if sshErr := writer.WriteSSHError(fmt.Sprintf("upload-pack failed: %v", err)); sshErr != nil {
			// Log the SSH error writing failure, but don't fail the request further
			fmt.Printf("Failed to write SSH error: %v\n", sshErr)
		}
		return
	}
}

// processSSHReceivePack processes the SSH receive-pack operation (push)
func (h *SSHProtocolHandler) processSSHReceivePack(w http.ResponseWriter, r *http.Request, sshReq *SSHRequest, repo git.Repository, opts *SSHStreamingOptions) {
	// Set content type for SSH receive-pack
	w.Header().Set("Content-Type", opts.ContentType)

	// Create SSH protocol writer for streaming
	writer := NewSSHProtocolWriter(w, opts.BufferSize)
	defer func() { _ = writer.Flush() }()

	// Parse SSH protocol headers
	protocolHeaders := ParseSSHGitProtocolHeaders(r)

	// Read request body for packfile data
	requestBody := r.Body
	if opts.MaxRequestSize > 0 {
		requestBody = http.MaxBytesReader(w, r.Body, opts.MaxRequestSize)
	}
	defer func() { _ = r.Body.Close() }()

	// For SSH receive-pack, we need to:
	// 1. Read the packfile from the request body
	// 2. Process the packfile and update references
	// 3. Return the appropriate status

	// For now, delegate to existing receive-pack logic
	err := h.executeSSHGitCommand(r.Context(), "git-receive-pack", sshReq.RepoPath, requestBody, writer, protocolHeaders)
	if err != nil {
		// Write SSH error format
		if sshErr := writer.WriteSSHError(fmt.Sprintf("receive-pack failed: %v", err)); sshErr != nil {
			// Log the SSH error writing failure, but don't fail the request further
			fmt.Printf("Failed to write SSH error: %v\n", sshErr)
		}
		return
	}
}

// executeSSHGitCommand executes a Git command for SSH operations
func (h *SSHProtocolHandler) executeSSHGitCommand(ctx context.Context, command, repoPath string, input io.ReadCloser, output *SSHProtocolWriter, headers map[string]string) error {
	// Validate the Git command
	if err := ValidateSSHGitCommand(command, repoPath); err != nil {
		return fmt.Errorf("invalid Git command: %w", err)
	}

	// For now, we'll use a simplified approach
	// In a full implementation, you would:
	// 1. Execute the actual git command using exec.Command
	// 2. Handle proper stdin/stdout/stderr streaming
	// 3. Process Git protocol negotiation
	// 4. Handle capability negotiation

	// Placeholder implementation - copy input to output with appropriate formatting
	buffer := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			n, err := input.Read(buffer)
			if err != nil {
				if err == io.EOF {
					return nil // Successfully processed all input
				}
				return fmt.Errorf("error reading input: %w", err)
			}

			if n > 0 {
				_, writeErr := output.Write(buffer[:n])
				if writeErr != nil {
					return fmt.Errorf("error writing output: %w", writeErr)
				}
			}
		}
	}
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

// isNilInterface checks if an interface contains a nil pointer
func isNilInterface(i interface{}) bool {
	if i == nil {
		return true
	}
	switch reflect.TypeOf(i).Kind() {
	case reflect.Ptr, reflect.Map, reflect.Array, reflect.Chan, reflect.Slice:
		return reflect.ValueOf(i).IsNil()
	}
	return false
}

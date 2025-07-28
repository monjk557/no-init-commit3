// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// This file handles receive-pack operations (push) as the executor counterpart to upload-pack (fetch).
package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// Repository lock manager for preventing concurrent pushes
var (
	repoLocks  = make(map[string]*sync.Mutex)
	locksMutex = sync.RWMutex{}
)

// ReceivePackRequest represents a parsed receive-pack request from the client
type ReceivePackRequest struct {
	Commands     []RefUpdateCommand
	Capabilities []string
	PackfileData []byte
}

// RefUpdateCommand represents a single reference update command in a push
type RefUpdateCommand struct {
	RefName string        // Reference name (e.g., refs/heads/main)
	OldHash plumbing.Hash // Current hash (zero hash for create)
	NewHash plumbing.Hash // New hash (zero hash for delete)
}

// ReceivePackHandler handles Git Smart HTTP protocol for push operations (executor side)
type ReceivePackHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
	errorHandler     *ErrorHandler
	capabilities     *ServerCapabilities
}

// NewReceivePackHandler creates a new receive-pack handler for the executor
func NewReceivePackHandler(cfg *config.Config, permClient permissions.PermissionClient) (*ReceivePackHandler, error) {
	// Initialize path configuration
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create error handler
	errorHandler := NewErrorHandler(nil) // Logger will be set if needed

	// Get server capabilities for receive-pack
	capabilities := DefaultServerCapabilities()

	return &ReceivePackHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
		errorHandler:     errorHandler,
		capabilities:     capabilities,
	}, nil
}

// HandleInfoRefs handles GET /repos/:owner/:repo/info/refs?service=git-receive-pack
// This endpoint advertises references and capabilities for push operations
func (h *ReceivePackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters from gateway-forwarded request
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	service := r.URL.Query().Get("service")

	// Validate service parameter
	if service != "git-receive-pack" {
		h.errorHandler.HandleError(w, r, ErrInvalidService, "receive_pack_info_refs")
		return
	}

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		h.errorHandler.HandleError(w, r, ErrMissingUserContext, "receive_pack_info_refs")
		return
	}

	// Check user permissions for WRITE access (pushes require write permission)
	hasAccess, err := h.permissionClient.CheckWriteAccess(ctx, userID, owner, repo)
	if err != nil {
		wrappedErr := fmt.Errorf("permission check failed: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack_info_refs")
		return
	}
	if !hasAccess {
		h.errorHandler.HandleError(w, r, ErrPermissionDenied, "receive_pack_info_refs")
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		wrappedErr := fmt.Errorf("invalid repository path: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack_info_refs")
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		h.errorHandler.HandleError(w, r, ErrRepositoryNotFound, "receive_pack_info_refs")
		return
	}

	// Open repository using go-git with caching
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to open repository: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack_info_refs")
		return
	}
	defer func() { _ = gitRepo.Close() }()

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Write service advertisement
	_, _ = fmt.Fprintf(w, "001f# service=git-receive-pack\n")
	_, _ = fmt.Fprintf(w, "0000") // flush packet

	// Write repository references with capabilities
	err = h.writeReferencesWithCapabilities(ctx, w, gitRepo)
	if err != nil {
		// If we've already started writing, we can't send an HTTP error
		// Log the error and write an error packet instead
		_, _ = fmt.Fprintf(w, "0000") // end packet
		return
	}

	// End with null packet
	_, _ = fmt.Fprintf(w, "0000")
}

// writeReferencesWithCapabilities writes Git references with server capabilities for receive-pack
func (h *ReceivePackHandler) writeReferencesWithCapabilities(ctx context.Context, w http.ResponseWriter, repo git.Repository) error {
	// Get all repository references
	refs, err := h.getAllReferences(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to get references: %w", err)
	}

	// Create packet line writer
	pktWriter := newPktLineWriter(w)

	// Create capability string for receive-pack operations
	capString := h.buildReceiveCapabilities()

	if len(refs) == 0 {
		// Empty repository - write capabilities on null ref
		line := fmt.Sprintf("0000000000000000000000000000000000000000 capabilities^{}\x00%s\n", capString)
		if err := pktWriter.writeLine(line); err != nil {
			return fmt.Errorf("failed to write capabilities: %w", err)
		}
	} else {
		// Sort references for consistent output
		sort.Slice(refs, func(i, j int) bool {
			return refs[i].Name < refs[j].Name
		})

		// Write first reference with capabilities
		firstRef := refs[0]
		line := fmt.Sprintf("%s %s\x00%s\n", firstRef.Hash, firstRef.Name, capString)
		if err := pktWriter.writeLine(line); err != nil {
			return fmt.Errorf("failed to write first ref with capabilities: %w", err)
		}

		// Write remaining references without capabilities
		for _, ref := range refs[1:] {
			line := fmt.Sprintf("%s %s\n", ref.Hash, ref.Name)
			if err := pktWriter.writeLine(line); err != nil {
				return fmt.Errorf("failed to write ref %s: %w", ref.Name, err)
			}
		}
	}

	return nil
}

// getAllReferences retrieves all references from the repository
func (h *ReceivePackHandler) getAllReferences(ctx context.Context, repo git.Repository) ([]Reference, error) {
	var refs []Reference

	// Try to get commits to find references
	commits, err := repo.GetCommits(ctx, "HEAD", 1, 0)
	if err != nil {
		// Repository might be empty - return empty refs list
		return refs, nil
	}

	if len(commits) > 0 {
		// For now, add HEAD and main branch
		headHash := commits[0].Hash

		refs = append(refs, Reference{
			Name: "HEAD",
			Hash: headHash,
		})

		refs = append(refs, Reference{
			Name: "refs/heads/main",
			Hash: headHash,
		})

		// TODO: In a full implementation, we would iterate through all refs
		// using go-git's References() method and convert them to our Reference struct
	}

	return refs, nil
}

// buildReceiveCapabilities builds the capability string for receive-pack operations
func (h *ReceivePackHandler) buildReceiveCapabilities() string {
	caps := []string{
		"report-status",                    // Send status for each ref update
		"delete-refs",                      // Allow reference deletion
		"ofs-delta",                        // Support offset deltas in packfiles
		"side-band-64k",                    // Support side-band protocol
		"atomic",                           // Support atomic transactions
		"push-options",                     // Support push options
		"agent=collabops-git-executor/1.0", // Server identification
	}

	return strings.Join(caps, " ")
}

// HandleReceivePack handles POST /repos/:owner/:repo/git-receive-pack
// This endpoint processes push requests with packfiles and updates references
func (h *ReceivePackHandler) HandleReceivePack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters from gateway-forwarded request
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		h.errorHandler.HandleError(w, r, ErrMissingUserContext, "receive_pack")
		return
	}

	// Check user permissions for WRITE access
	hasAccess, err := h.permissionClient.CheckWriteAccess(ctx, userID, owner, repo)
	if err != nil {
		wrappedErr := fmt.Errorf("permission check failed: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack")
		return
	}
	if !hasAccess {
		h.errorHandler.HandleError(w, r, ErrPermissionDenied, "receive_pack")
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		wrappedErr := fmt.Errorf("invalid repository path: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack")
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		h.errorHandler.HandleError(w, r, ErrRepositoryNotFound, "receive_pack")
		return
	}

	// Parse the receive-pack request from client (forwarded by gateway)
	receiveReq, err := h.parseReceivePackRequest(r.Body)
	if err != nil {
		wrappedErr := fmt.Errorf("invalid receive-pack request: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack")
		return
	}

	// Acquire repository lock to prevent concurrent pushes
	repoLock := getRepositoryLock(repoPath)
	repoLock.Lock()
	defer repoLock.Unlock()

	// Open repository using go-git
	fs := osfs.New(repoPath)
	storage := filesystem.NewStorage(fs, nil)
	gitRepo, err := gogit.Open(storage, fs)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to open repository: %w", err)
		h.errorHandler.HandleError(w, r, wrappedErr, "receive_pack")
		return
	}

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// Process the push request
	err = h.processReceivePackRequest(ctx, gitRepo, receiveReq, w)
	if err != nil {
		// If we've started writing, we can't send HTTP error anymore
		// Write an error in protocol format
		pktWriter := newPktLineWriter(w)
		_ = pktWriter.writeLine(fmt.Sprintf("unpack %s", err.Error()))
		_ = pktWriter.flush()
		return
	}
}

// parseReceivePackRequest parses the receive-pack request body in pkt-line format
func (h *ReceivePackHandler) parseReceivePackRequest(body io.Reader) (*ReceivePackRequest, error) {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	scanner := bufio.NewScanner(body)
	firstCommand := true
	var packfileBuffer bytes.Buffer
	inPackfile := false

	for scanner.Scan() {
		line := scanner.Text()

		// Handle empty lines or flush packets
		if line == "" || line == "0000" {
			continue
		}

		// Check if we've hit the packfile data
		if strings.HasPrefix(line, "PACK") || inPackfile {
			inPackfile = true
			packfileBuffer.WriteString(line)
			packfileBuffer.WriteByte('\n')
			continue
		}

		// Parse pkt-line format
		if len(line) < 4 {
			continue
		}

		// Extract length prefix (first 4 hex characters)
		lengthHex := line[:4]
		length, err := strconv.ParseInt(lengthHex, 16, 32)
		if err != nil {
			continue // Skip malformed lines
		}

		if length == 0 {
			// Flush packet, indicates end of commands
			break
		}

		// Extract actual content (skip length prefix)
		content := line[4:]
		if len(content) == 0 {
			continue
		}

		// Parse command line: "old-hash new-hash ref-name [capabilities]"
		// Need to be careful with parsing since capabilities can be null-separated OR space-separated

		// First, split only on the first two spaces to get hashes
		firstSpaceIdx := strings.Index(content, " ")
		if firstSpaceIdx == -1 || len(content) <= firstSpaceIdx+1 {
			continue
		}

		secondSpaceIdx := strings.Index(content[firstSpaceIdx+1:], " ")
		if secondSpaceIdx == -1 {
			continue
		}
		secondSpaceIdx += firstSpaceIdx + 1

		oldHashStr := content[:firstSpaceIdx]
		newHashStr := content[firstSpaceIdx+1 : secondSpaceIdx]
		refAndCapsStr := content[secondSpaceIdx+1:]

		// Check for capabilities in the ref name (null-separated)
		var refName string
		var capabilities []string

		if strings.Contains(refAndCapsStr, "\x00") {
			// Null-separated format: "refs/heads/main\x00report-status delete-refs"
			refParts := strings.Split(refAndCapsStr, "\x00")
			refName = refParts[0]
			if len(refParts) > 1 {
				// All remaining parts after null are capabilities (space-separated)
				capStr := strings.Join(refParts[1:], " ")
				capabilities = strings.Fields(capStr)
			}
		} else {
			// Space-separated format: parse normally
			parts := strings.Fields(refAndCapsStr)
			if len(parts) > 0 {
				refName = parts[0]
				if len(parts) > 1 {
					capabilities = parts[1:]
				}
			}
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

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading request body: %w", err)
	}

	// Store packfile data if any
	if packfileBuffer.Len() > 0 {
		req.PackfileData = packfileBuffer.Bytes()
	}

	return req, nil
}

// processReceivePackRequest processes the push request and updates references
func (h *ReceivePackHandler) processReceivePackRequest(ctx context.Context, repo *gogit.Repository, req *ReceivePackRequest, w http.ResponseWriter) error {
	pktWriter := newPktLineWriter(w)

	// Validate that we have commands to process
	if len(req.Commands) == 0 {
		if err := pktWriter.writeLine("unpack ok"); err != nil {
			return fmt.Errorf("failed to write unpack status: %w", err)
		}
		return pktWriter.flush()
	}

	// Process packfile if present
	if len(req.PackfileData) > 0 {
		err := h.processPackfile(ctx, repo, req.PackfileData)
		if err != nil {
			// Report unpack failure
			if err := pktWriter.writeLine(fmt.Sprintf("unpack %s", err.Error())); err != nil {
				return fmt.Errorf("failed to write unpack error: %w", err)
			}
			return pktWriter.flush()
		}
	}

	// Report successful unpack
	if err := pktWriter.writeLine("unpack ok"); err != nil {
		return fmt.Errorf("failed to write unpack status: %w", err)
	}

	// Process each reference update command
	for _, cmd := range req.Commands {
		err := h.processRefUpdate(ctx, repo, cmd)
		if err != nil {
			// Report failure for this reference
			statusLine := fmt.Sprintf("ng %s %s", cmd.RefName, err.Error())
			if err := pktWriter.writeLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref error status: %w", err)
			}
		} else {
			// Report success for this reference
			statusLine := fmt.Sprintf("ok %s", cmd.RefName)
			if err := pktWriter.writeLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref success status: %w", err)
			}
		}
	}

	// End with flush packet
	return pktWriter.flush()
}

// processPackfile processes the uploaded packfile and stores objects in the repository
func (h *ReceivePackHandler) processPackfile(ctx context.Context, repo *gogit.Repository, packfileData []byte) error {
	if len(packfileData) == 0 {
		return nil // No packfile to process
	}

	// Validate packfile format and extract basic information
	header, err := h.validatePackfileFormat(packfileData)
	if err != nil {
		return fmt.Errorf("invalid packfile format: %w", err)
	}

	// For now, if the packfile is valid, we assume the objects will be properly handled
	// by the reference update process. A complete implementation would:
	// 1. Parse individual objects from the packfile
	// 2. Resolve delta objects
	// 3. Store objects in the repository
	// 4. Update indexes

	// Log successful validation
	if header.ObjectCount > 0 {
		// Objects will be validated during reference updates
		_ = header.ObjectCount
	}

	return nil
}

// PackfileHeader represents basic packfile information
type PackfileHeader struct {
	Version     uint32
	ObjectCount uint32
}

// validatePackfileFormat performs basic validation of packfile format
func (h *ReceivePackHandler) validatePackfileFormat(packfileData []byte) (*PackfileHeader, error) {
	if len(packfileData) < 12 {
		return nil, fmt.Errorf("packfile too short: need at least 12 bytes, got %d", len(packfileData))
	}

	// Check packfile signature (first 4 bytes should be "PACK")
	signature := string(packfileData[0:4])
	if signature != "PACK" {
		return nil, fmt.Errorf("invalid packfile signature: expected 'PACK', got '%s'", signature)
	}

	// Read version (bytes 4-7, big endian)
	version := binary.BigEndian.Uint32(packfileData[4:8])
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported packfile version: %d", version)
	}

	// Read object count (bytes 8-11, big endian)
	objectCount := binary.BigEndian.Uint32(packfileData[8:12])

	return &PackfileHeader{
		Version:     version,
		ObjectCount: objectCount,
	}, nil
}

// processRefUpdate processes a single reference update command
func (h *ReceivePackHandler) processRefUpdate(ctx context.Context, repo *gogit.Repository, cmd RefUpdateCommand) error {
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
	}

	return nil
}

// isValidRefName validates a Git reference name according to Git naming rules
func isValidRefName(refName string) bool {
	// Basic validation - Git has complex rules, but these cover most cases
	if refName == "" {
		return false
	}

	// Must start with refs/
	if !strings.HasPrefix(refName, "refs/") {
		return false
	}

	// Check for control characters (ASCII 0-31)
	for _, r := range refName {
		if r >= 0 && r <= 31 {
			return false
		}
	}

	// Cannot contain certain characters
	invalidChars := []string{" ", "~", "^", ":", "?", "*", "[", "\\", "..", "//", "@{"}
	for _, char := range invalidChars {
		if strings.Contains(refName, char) {
			return false
		}
	}

	// Cannot end with certain characters
	if strings.HasSuffix(refName, "/") || strings.HasSuffix(refName, ".lock") {
		return false
	}

	return true
}

// getRepositoryLock gets or creates a mutex for the specific repository path
func getRepositoryLock(repoPath string) *sync.Mutex {
	locksMutex.RLock()
	lock, exists := repoLocks[repoPath]
	locksMutex.RUnlock()

	if exists {
		return lock
	}

	// Create new lock
	locksMutex.Lock()
	defer locksMutex.Unlock()

	// Double-check in case another goroutine created it
	if lock, exists := repoLocks[repoPath]; exists {
		return lock
	}

	lock = &sync.Mutex{}
	repoLocks[repoPath] = lock
	return lock
}

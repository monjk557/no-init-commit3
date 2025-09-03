// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// These handlers receive requests forwarded from the collabops-git-http gateway
// and implement the core Git protocol logic using go-git.
package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// UploadPackRequest represents a parsed upload-pack request from the client
type UploadPackRequest struct {
	Wants        []string
	Haves        []string
	Capabilities []string
	Done         bool
}

// pktLineWriter helps write Git protocol packet lines
type pktLineWriter struct {
	w io.Writer
}

// UploadPackHandler handles Git Smart HTTP protocol for fetch operations (executor side)
type UploadPackHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// NewUploadPackHandler creates a new upload-pack handler for the executor
func NewUploadPackHandler(cfg *config.Config) (*UploadPackHandler, error) {
	// Initialize path configuration
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &UploadPackHandler{
		pathConfig: pathConfig,
	}, nil
}

// HandleInfoRefs handles GET /repos/:owner/:repo/info/refs?service=git-upload-pack
// This endpoint advertises references and capabilities to the gateway (which forwards to client)
func (h *UploadPackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters from gateway-forwarded request
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	service := r.URL.Query().Get("service")

	// Validate service parameter
	if service != "git-upload-pack" {
		http.Error(w, "Invalid service parameter", http.StatusBadRequest)
		return
	}

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication context", http.StatusUnauthorized)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Open repository using go-git with simple caching
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = gitRepo.Close() }()

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Write service advertisement
	_, _ = fmt.Fprintf(w, "001e# service=git-upload-pack\n")
	_, _ = fmt.Fprintf(w, "0000") // flush packet

	// Get repository references using our git package
	// For now, we'll implement a basic version that gets HEAD and branches
	err = h.writeReferences(ctx, w, gitRepo)
	if err != nil {
		// If we've already started writing, we can't send an HTTP error
		// Log the error and write an error packet instead
		_, _ = fmt.Fprintf(w, "0000") // end packet
		return
	}

	// End with null packet
	_, _ = fmt.Fprintf(w, "0000")
}

// writeReferences writes Git references in the Smart HTTP protocol format
func (h *UploadPackHandler) writeReferences(ctx context.Context, w http.ResponseWriter, repo git.Repository) error {
	// This is a simplified implementation. In a full implementation,
	// we would use go-git to get all references and format them properly
	// For now, we'll implement basic functionality to get started

	// Try to get some basic repository information
	// We'll enhance this as we develop the git package further

	// Get commits to find HEAD reference
	commits, err := repo.GetCommits(ctx, "HEAD", 1, 0)
	if err != nil {
		// Repository might be empty or HEAD might not exist
		// Write a dummy reference to satisfy the protocol
		line := "0000000000000000000000000000000000000000 capabilities^{}\x00multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor\n"
		pktLine := fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))
		return nil
	}

	if len(commits) > 0 {
		// Write HEAD reference with capabilities
		headHash := commits[0].Sha
		line := fmt.Sprintf("%s HEAD\x00multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor\n", headHash)
		pktLine := fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))

		// Write main branch reference (without capabilities)
		line = fmt.Sprintf("%s refs/heads/main\n", headHash)
		pktLine = fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))
	}

	return nil
}

// HandleUploadPack handles POST /repos/:owner/:repo/git-upload-pack
// This endpoint processes want/have negotiation and streams packfiles to the gateway
func (h *UploadPackHandler) HandleUploadPack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters from gateway-forwarded request
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication context", http.StatusUnauthorized)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Parse the upload-pack request from client (forwarded by gateway)
	uploadReq, err := h.parseUploadPackRequest(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid upload-pack request: %v", err), http.StatusBadRequest)
		return
	}

	// Open repository using go-git
	fs := osfs.New(repoPath)
	storage := filesystem.NewStorage(fs, nil)
	gitRepo, err := gogit.Open(storage, fs)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// Create a packet line writer
	pktWriter := &pktLineWriter{w: w}

	// Process want/have negotiation
	err = h.processWantHaveNegotiation(ctx, gitRepo, uploadReq, pktWriter)
	if err != nil {
		// If we've started writing, we can't send HTTP error anymore
		// Write an error packet instead
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
}

// parseUploadPackRequest parses the upload-pack request body in pkt-line format
func (h *UploadPackHandler) parseUploadPackRequest(body io.Reader) (*UploadPackRequest, error) {
	req := &UploadPackRequest{
		Wants: make([]string, 0),
		Haves: make([]string, 0),
	}

	buf := make([]byte, 4) // Buffer for reading length header
	firstWant := true

	for {
		// Read 4-byte length header
		n, err := io.ReadFull(body, buf)
		if err != nil {
			if err == io.EOF {
				break // End of input
			}
			return req, err
		}
		if n != 4 {
			break
		}

		// Parse length
		lengthHex := string(buf)
		length, err := strconv.ParseInt(lengthHex, 16, 32)
		if err != nil {
			continue // Skip invalid packet
		}

		if length == 0 {
			// Flush packet (0000)
			continue
		}

		if length < 4 {
			// Invalid packet length
			continue
		}

		// Read the data portion (length - 4 for the header we already read)
		dataLen := int(length) - 4
		if dataLen <= 0 {
			continue
		}

		dataBuf := make([]byte, dataLen)
		n, err = io.ReadFull(body, dataBuf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return req, err
		}
		if n != dataLen {
			continue
		}

		// Convert to string and trim newline if present
		data := strings.TrimSuffix(string(dataBuf), "\n")

		if strings.HasPrefix(data, "want ") {
			parts := strings.Fields(data)
			if len(parts) >= 2 {
				req.Wants = append(req.Wants, parts[1])

				// Capabilities are sent with the first want line
				if firstWant && len(parts) > 2 {
					req.Capabilities = parts[2:]
					firstWant = false
				}
			}
		} else if strings.HasPrefix(data, "have ") {
			parts := strings.Fields(data)
			if len(parts) >= 2 {
				req.Haves = append(req.Haves, parts[1])
			}
		} else if strings.TrimSpace(data) == "done" {
			req.Done = true
			break
		}
	}

	return req, nil
}

// processWantHaveNegotiation handles the want/have negotiation and packfile generation
func (h *UploadPackHandler) processWantHaveNegotiation(ctx context.Context, repo *gogit.Repository, req *UploadPackRequest, pktWriter *pktLineWriter) error {
	// For now, implement a simple version that doesn't do complex negotiation
	// A full implementation would check for common commits and optimize the packfile

	if len(req.Wants) == 0 {
		return fmt.Errorf("no wants specified")
	}

	// Check if all wanted objects exist
	for _, want := range req.Wants {
		hash := plumbing.NewHash(want)
		_, err := repo.Object(plumbing.AnyObject, hash)
		if err != nil {
			return fmt.Errorf("object %s not found: %w", want, err)
		}
	}

	// Send NAK (no acknowledgment) - in a full implementation, we'd check for common commits
	if err := pktWriter.writeLine("NAK"); err != nil {
		return fmt.Errorf("failed to write NAK: %w", err)
	}

	// Generate and stream a simplified packfile
	// Note: This is a basic implementation. A production version would generate
	// proper packfiles with object deltas and compression
	err := h.generateAndStreamPackfile(ctx, repo, req, pktWriter)
	if err != nil {
		return fmt.Errorf("failed to generate packfile: %w", err)
	}

	return nil
}

// generateAndStreamPackfile generates and streams a packfile for the requested objects
func (h *UploadPackHandler) generateAndStreamPackfile(ctx context.Context, repo *gogit.Repository, req *UploadPackRequest, pktWriter *pktLineWriter) error {
	// Send sideband packet indicating packfile data follows
	if err := pktWriter.writeLine("0008NAK\n"); err != nil {
		return err
	}

	// Flush to end the negotiation phase
	if err := pktWriter.flush(); err != nil {
		return err
	}

	// For this basic implementation, we'll collect the objects and send them
	// In a production system, this would generate a proper Git packfile format
	objectsToSend := make(map[plumbing.Hash]*object.Commit)

	// Collect all commits reachable from wants
	for _, want := range req.Wants {
		hash := plumbing.NewHash(want)
		commit, err := repo.CommitObject(hash)
		if err != nil {
			continue // Skip if not a commit object
		}

		// Walk the commit history to collect all reachable commits
		err = h.walkCommitHistory(repo, commit, objectsToSend, req.Haves)
		if err != nil {
			return fmt.Errorf("failed to walk commit history: %w", err)
		}
	}

	// Send a minimal "packfile" (this is simplified - real packfiles are binary format)
	packfileHeader := fmt.Sprintf("# pack with %d objects\n", len(objectsToSend))
	if err := pktWriter.writeLine(packfileHeader); err != nil {
		return err
	}

	// Send object information (simplified)
	for hash, commit := range objectsToSend {
		objectInfo := fmt.Sprintf("commit %s %s\n", hash.String(), commit.Message)
		if err := pktWriter.writeLine(objectInfo); err != nil {
			return err
		}
	}

	// End with flush packet
	return pktWriter.flush()
}

// walkCommitHistory walks the commit history and collects commits
func (h *UploadPackHandler) walkCommitHistory(repo *gogit.Repository, commit *object.Commit, collected map[plumbing.Hash]*object.Commit, haves []string) error {
	hash := commit.Hash

	// Check if we've already processed this commit
	if _, exists := collected[hash]; exists {
		return nil
	}

	// Check if this commit is in the "have" list (client already has it)
	for _, have := range haves {
		if hash.String() == have {
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

			err = h.walkCommitHistory(repo, parent, collected, haves)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// writeLine writes a packet line with the data
func (p *pktLineWriter) writeLine(data string) error {
	// Handle empty data as flush packet (Git protocol compliance)
	if data == "" {
		return p.flush()
	}

	// Add newline if not present (for normal Git protocol lines)
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}

	// Calculate packet length (4 bytes for length + data)
	length := len(data) + 4

	// Check maximum packet line length (Git protocol limit)
	if length > 65520 {
		return fmt.Errorf("packet line too long: %d bytes (max 65520)", length)
	}

	line := fmt.Sprintf("%04x%s", length, data)

	_, err := p.w.Write([]byte(line))
	return err
}

// flush writes a flush packet (0000)
func (p *pktLineWriter) flush() error {
	_, err := p.w.Write([]byte("0000"))
	return err
}

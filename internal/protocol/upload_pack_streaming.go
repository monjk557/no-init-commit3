package protocol

import (
	"fmt"
	"net/http"

	"github.com/collabops/git-executor/internal/streaming"
	"github.com/go-chi/chi/v5"
	gogit "github.com/go-git/go-git/v5"
)

// StreamingUploadPackHandler provides streaming-optimized upload-pack functionality
type StreamingUploadPackHandler struct {
	*UploadPackHandler
}

// NewStreamingUploadPackHandler creates a new streaming upload-pack handler
func NewStreamingUploadPackHandler(base *UploadPackHandler) *StreamingUploadPackHandler {
	return &StreamingUploadPackHandler{
		UploadPackHandler: base,
	}
}

// HandleUploadPackStreaming processes upload-pack requests with efficient streaming
func (h *StreamingUploadPackHandler) HandleUploadPackStreaming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	// Extract path parameters from gateway-forwarded request
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication context", http.StatusUnauthorized)
		return
	}

	// Check user permissions for read access
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := h.getValidatedRepoPath(owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Open repository using go-git
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Parse the upload-pack request
	uploadReq, err := h.parseUploadPackRequest(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid request format: %v", err), http.StatusBadRequest)
		return
	}

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// Create streaming request
	streamReq := &streaming.PackfileStreamRequest{
		Wants:        uploadReq.Wants,
		Haves:        uploadReq.Haves,
		Capabilities: uploadReq.Capabilities,
	}

	// Create Git streamer and stream the packfile
	streamer := streaming.NewGitStreamer(gitRepo)
	err = streamer.StreamPackfile(ctx, streamReq, w)
	if err != nil {
		// If we've already started writing, we can't send HTTP error anymore
		// The streaming mechanism will handle internal errors gracefully
		fmt.Printf("Streaming error: %v\n", err)
		return
	}
}

// HandleInfoRefsStreaming provides streaming-optimized info/refs functionality
func (h *StreamingUploadPackHandler) HandleInfoRefsStreaming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	// Extract path parameters from gateway-forwarded request
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
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

	// Check user permissions for read access
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := h.getValidatedRepoPath(owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Open repository using go-git
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Write service advertisement
	fmt.Fprintf(w, "001e# service=git-upload-pack\n")
	fmt.Fprintf(w, "0000") // flush packet

	// Create Git streamer and stream references
	streamer := streaming.NewGitStreamer(gitRepo)
	capabilities := []string{"multi_ack", "thin-pack", "side-band", "side-band-64k", "ofs-delta", "shallow", "no-progress", "include-tag", "multi_ack_detailed", "no-done", "symref=HEAD:refs/heads/main", "agent=git/collabops-git-executor"}
	
	err = streamer.StreamRefs(ctx, w, capabilities)
	if err != nil {
		// If we've already started writing, we can't send HTTP error anymore
		fmt.Printf("Reference streaming error: %v\n", err)
		return
	}

	// End with null packet
	fmt.Fprintf(w, "0000")
}

// getValidatedRepoPath gets and validates the repository path (helper method)
func (h *StreamingUploadPackHandler) getValidatedRepoPath(owner, repo string) (string, error) {
	// Use the existing path validation from the base handler
	return h.pathConfig.FormatRepoPath(owner, repo), nil
}

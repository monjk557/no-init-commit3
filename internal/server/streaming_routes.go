package server

import (
	"fmt"

	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/go-chi/chi/v5"
)

// setupStreamingGitRoutes configures Git Smart HTTP protocol routes with streaming optimization
func (s *Server) setupStreamingGitRoutes(router *chi.Mux) {
	// Initialize permission client with default configuration
	// TODO: This should be properly configured with actual API service URL from config
	permConfig := permissions.DefaultConfig()
	permConfig.APIServiceURL = "http://api-service.local" // Override with specific URL

	permClient, err := permissions.NewPermissionClient(permConfig)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize permission client for streaming: %v\n", err)
		return
	}

	// Initialize base upload-pack handler
	uploadPackHandler, err := protocol.NewUploadPackHandler(s.config, permClient)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize upload-pack handler for streaming: %v\n", err)
		return
	}

	// Create streaming upload-pack handler
	streamingHandler := protocol.NewStreamingUploadPackHandler(uploadPackHandler)

	// Add streaming routes as alternative endpoints (not conflicting with existing ones)
	router.Route("/streaming/{owner}/{repo}", func(r chi.Router) {
		// GET /repos/streaming/{owner}/{repo}/info/refs?service=git-upload-pack
		r.Get("/info/refs", streamingHandler.HandleInfoRefsStreaming)

		// POST /repos/streaming/{owner}/{repo}/git-upload-pack
		r.Post("/git-upload-pack", streamingHandler.HandleUploadPackStreaming)
	})

	fmt.Printf("✅ Streaming Git Smart HTTP routes configured at /streaming/{owner}/{repo}\n")
}

// enableStreamingOptimization adds streaming routes alongside regular routes
func (s *Server) enableStreamingOptimization(apiRouter *chi.Mux) {
	fmt.Printf("🚀 Enabling streaming optimization for Git operations\n")
	s.setupStreamingGitRoutes(apiRouter)
}

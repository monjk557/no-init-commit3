package server

// TODO: Streaming optimization features - commenting out unused functions for now

// setupStreamingGitRoutes configures streaming Git operation routes
// func (s *Server) setupStreamingGitRoutes(router *chi.Mux) {
// 	// Create permission client
// 	permClient, err := permissions.NewPermissionClient(permissions.DefaultConfig())
// 	if err != nil {
// 		fmt.Printf("Warning: Failed to create permission client for streaming routes: %v\n", err)
// 		return
// 	}
//
// 	// Create streaming protocol handler
// 	streamingHandler, err := protocol.NewStreamingProtocolHandler(s.config, permClient)
// 	if err != nil {
// 		fmt.Printf("Warning: Failed to create streaming protocol handler: %v\n", err)
// 		return
// 	}
//
// 	router.Route("/streaming", func(r chi.Router) {
// 		// Streaming clone operations
// 		r.Get("/{owner}/{repo}/clone", streamingHandler.HandleStreamingClone)
//
// 		// Streaming fetch operations
// 		r.Get("/{owner}/{repo}/fetch", streamingHandler.HandleStreamingFetch)
//
// 		// Streaming diff operations
// 		r.Get("/{owner}/{repo}/diff", streamingHandler.HandleStreamingDiff)
// 	})
// }

// enableStreamingOptimization sets up streaming optimization for large operations
// func (s *Server) enableStreamingOptimization(apiRouter *chi.Mux) {
// 	// Create streaming middleware for large repository operations
// 	streamingMiddleware := func(next http.Handler) http.Handler {
// 		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 			// Check if request is for a large repository operation
// 			if strings.Contains(r.URL.Path, "/diff") ||
// 				strings.Contains(r.URL.Path, "/commits") ||
// 				strings.Contains(r.URL.Path, "/tree") {
//
// 				// Enable streaming response
// 				w.Header().Set("Transfer-Encoding", "chunked")
// 				w.Header().Set("Cache-Control", "no-cache")
// 			}
// 			next.ServeHTTP(w, r)
// 		})
// 	}
//
// 	// Apply streaming middleware to API routes
// 	apiRouter.Use(streamingMiddleware)
// }

package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/collabops/git-executor/internal/config"
	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server represents the HTTP server
type Server struct {
	server *http.Server
	config *config.Config
}

// New creates a new HTTP server instance
func New(cfg *config.Config) *Server {
	return &Server{
		config: cfg,
	}
}

// Start initializes and starts the HTTP server
func (s *Server) Start() error {
	// Create router
	router := s.setupRouter()

	// Create HTTP server
	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:      router,
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
		IdleTimeout:  30 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		fmt.Printf("🚀 Server starting on %s%s\n", s.server.Addr, s.config.Server.BasePath)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("❌ Server failed to start: %v\n", err)
		}
	}()

	// Wait for shutdown signal
	return s.waitForShutdown()
}

// setupRouter configures the HTTP router with middleware and routes
func (s *Server) setupRouter() *chi.Mux {
	router := chi.NewRouter()

	// Add middleware
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// Health check endpoint
	router.Get("/health", s.healthCheck)

	// Mount API routes under base path if configured
	if s.config.Server.BasePath != "/" {
		router.Mount(s.config.Server.BasePath, s.setupAPIRoutes())
	} else {
		router.Mount("/", s.setupAPIRoutes())
	}

	return router
}

// setupAPIRoutes configures the API-specific routes
func (s *Server) setupAPIRoutes() *chi.Mux {
	apiRouter := chi.NewRouter()

	// Placeholder for future API routes
	apiRouter.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"message": "CollabOps Git Executor API", "version": "1.0.0", "status": "ready"}`)
	})

	return apiRouter
}

// healthCheck handles the health check endpoint
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "healthy", "timestamp": "%s"}`, time.Now().UTC().Format(time.RFC3339))
}

// waitForShutdown waits for shutdown signals and performs graceful shutdown
func (s *Server) waitForShutdown() error {
	// Create channel to listen for interrupt signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Block until a signal is received
	sig := <-quit
	fmt.Printf("\n🛑 Received signal: %v\n", sig)

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Attempt graceful shutdown
	fmt.Println("🔄 Shutting down server gracefully...")
	if err := s.server.Shutdown(ctx); err != nil {
		fmt.Printf("❌ Server forced to shutdown: %v\n", err)
		return err
	}

	fmt.Println("✅ Server shutdown complete")
	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// GetAddr returns the server address
func (s *Server) GetAddr() string {
	if s.server == nil {
		return ""
	}
	return s.server.Addr
}

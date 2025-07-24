package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// Server represents the HTTP server
type Server struct {
	server *http.Server
	config *config.Config
	logger *logging.Logger
}

// New creates a new HTTP server instance
func New(cfg *config.Config) *Server {
	return &Server{
		config: cfg,
		logger: logging.GetGlobal(),
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
		IdleTimeout:  s.config.Server.IdleTimeout,
	}

	// Check if port is available
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		s.logger.Error("Failed to bind to address",
			zap.String("address", s.server.Addr),
			zap.Error(err))
		return fmt.Errorf("failed to bind to address %s: %w", s.server.Addr, err)
	}
	_ = listener.Close()

	// Start server in a goroutine
	go func() {
		s.logger.Info("Server starting",
			zap.String("address", s.server.Addr),
			zap.String("basePath", s.config.Server.BasePath))
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Server failed to start", zap.Error(err))
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
	router.Use(s.loggingMiddleware)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// Health check endpoint
	router.Get(s.config.Server.HealthCheckPath, s.healthCheck)

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

	// Basic API info endpoint
	apiRouter.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"message": "CollabOps Git Executor API", "version": "1.0.0", "status": "ready"}`)
	})

	// Git Smart HTTP Protocol routes
	s.setupGitRoutes(apiRouter)

	return apiRouter
}

// setupGitRoutes configures Git Smart HTTP protocol routes
func (s *Server) setupGitRoutes(router *chi.Mux) {
	// Initialize permission client (for now, we'll create a basic config)
	// TODO: This should be properly configured with actual API service URL
	permConfig := permissions.DefaultConfig()
	permConfig.APIServiceURL = "http://api-service.local" // Override with specific URL
	permClient, err := permissions.NewPermissionClient(permConfig)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize permission client: %v\n", err)
		return
	}

	// Initialize upload-pack handler
	uploadPackHandler, err := protocol.NewUploadPackHandler(s.config, permClient)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize upload-pack handler: %v\n", err)
		return
	}

	// Git Smart HTTP routes for fetch operations (executor endpoints)
	router.Route("/{owner}/{repo}", func(r chi.Router) {
		// GET /repos/{owner}/{repo}/info/refs?service=git-upload-pack
		r.Get("/info/refs", uploadPackHandler.HandleInfoRefs)

		// POST /repos/{owner}/{repo}/git-upload-pack (to be implemented in subtask 9.2)
		r.Post("/git-upload-pack", uploadPackHandler.HandleUploadPack)
	})
}

// healthCheck handles the health check endpoint
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"status": "healthy", "timestamp": "%s"}`, time.Now().UTC().Format(time.RFC3339))
}

// waitForShutdown waits for shutdown signals and performs graceful shutdown
func (s *Server) waitForShutdown() error {
	// Create channel to listen for interrupt signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Block until a signal is received
	sig := <-quit
	s.logger.Info("Received shutdown signal", zap.String("signal", sig.String()))

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
	defer cancel()

	// Attempt graceful shutdown
	s.logger.Info("Shutting down server gracefully")
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Error("Server forced to shutdown", zap.Error(err))
		return err
	}

	s.logger.Info("Server shutdown complete")
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

// loggingMiddleware logs HTTP requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		s.logger.Info("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", ww.Status()),
			zap.Int("bytes", ww.BytesWritten()),
			zap.Duration("duration", time.Since(start)),
			zap.String("remote", r.RemoteAddr),
		)
	})
}

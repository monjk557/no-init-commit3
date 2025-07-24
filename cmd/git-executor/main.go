package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/server"
	"go.uber.org/zap"
)

func main() {
	// Parse CLI flags
	var (
		configPath  = flag.String("config", "", "Path to configuration file")
		healthCheck = flag.Bool("health-check", false, "Perform health check and exit")
	)
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	logging.SetGlobal(logger)

	// Handle health check
	if *healthCheck {
		// TODO: Implement actual health check logic
		// For now, just check if we can load config and initialize logger
		logger.Info("Health check passed")
		os.Exit(0)
	}

	// Create and start server
	srv := server.New(cfg)
	if err := srv.Start(); err != nil {
		logger.Error("Server failed to start", zap.Error(err))
		os.Exit(1)
	}
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/server"
	"go.uber.org/zap"
)

func main() {
	// Parse CLI flags
	// Go실행할때 port를 준다던지, 읽어준다는지 하는 거를 cli option으로 줌.
	// Viper가 다 해줌.
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
	// 이 프로그램에서 이 로거를 전역으로 쓸것이다. 함수로 쓰면 불편하니까 어디서든 쓸수있게.
	logging.SetGlobal(logger)

	// Handle health check
	if *healthCheck {
		// TODO: Implement actual health check logic
		// For now, just check if we can load config and initialize logger
		logger.Info("Health check passed")
		os.Exit(0)
	}

	// Create HTTP server
	srv, err := server.New(cfg)
	if err != nil {
		logger.Error("Failed to create server", zap.Error(err))
		os.Exit(1)
	}

	// Create TCP server
	tcpSrv, err := server.NewDuplexTCPServer(cfg)
	if err != nil {
		logger.Error("Failed to create TCP server", zap.Error(err))
		os.Exit(1)
	}

	// Start HTTP server in goroutine
	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("HTTP server failed to start", zap.Error(err))
			os.Exit(1)
		}
	}()

	// Start TCP server in goroutine
	go func() {
		if err := tcpSrv.Start(); err != nil {
			logger.Error("TCP server failed to start", zap.Error(err))
			os.Exit(1)
		}
	}()

	// Wait for signals to shutdown both servers
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Received shutdown signal, shutting down gracefully")

	// Shutdown both servers
	ctx := context.Background()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Failed to shutdown HTTP server", zap.Error(err))
	}
	if err := tcpSrv.Stop(); err != nil {
		logger.Error("Failed to shutdown TCP server", zap.Error(err))
	}

}

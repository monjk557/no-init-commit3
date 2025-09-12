// Git Executor 서비스의 메인 진입점
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
	// CLI 플래그 파싱 - 설정 파일 경로와 헬스체크 옵션을 처리
	var (
		configPath  = flag.String("config", "", "Path to configuration file")
		healthCheck = flag.Bool("health-check", false, "Perform health check and exit")
	)
	flag.Parse()

	// 설정 파일 로드 - YAML 형식의 설정 파일을 읽어서 Config 구조체로 변환
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// 로거 초기화 - zap 로거를 생성하고 설정
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	// 전역 로거 설정 - 애플리케이션 전체에서 사용할 수 있도록 설정
	logging.SetGlobal(logger)

	// 헬스체크 모드 처리 - health-check 플래그가 있으면 상태 확인 후 종료
	if *healthCheck {
		// TODO: 실제 헬스체크 로직 구현 필요
		// 현재는 설정 로드와 로거 초기화만 확인
		logger.Info("Health check passed")
		os.Exit(0)
	}

	// Git 서버 생성 - HTTP/SSH Git 프로토콜을 처리하는 서버 인스턴스 생성
	srv, err := server.New(cfg)
	if err != nil {
		logger.Error("Failed to create server", zap.Error(err))
		os.Exit(1)
	}

	// 서버 시작 - 설정된 포트에서 요청 수신 시작
	if err := srv.Start(); err != nil {
		logger.Error("Server failed to start", zap.Error(err))
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

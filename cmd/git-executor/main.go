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
	"github.com/collabops/git-executor/internal/connection"
	"github.com/collabops/git-executor/internal/hooks"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/maintenance"
	"github.com/collabops/git-executor/internal/server"
	"go.uber.org/zap"
)

func main() {
	// pre-receive hook mode: when git invokes this binary as the pre-receive
	// hook (argv[0] basename), do ONLY the hook and exit — no config/logger/server
	// load. Context arrives via GITEXEC_HOOK_* env set by the receive-pack spawn.
	if hooks.IsPreReceiveInvocation() {
		os.Exit(hooks.RunPreReceive())
	}

	// CLI 플래그 파싱 - 헬스체크 옵션을 처리
	var (
		healthCheck = flag.Bool("health-check", false, "Perform health check and exit")
	)
	flag.Parse()

	// 환경변수 파일(.env) 로드 - OS의 환경변수를 읽거나 기본 변수를 사용하여 Config 구조체로 변환
	cfg, err := config.Load()
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

	logger.Info("Application environment loaded",
		zap.String("app_env", string(cfg.Env)),
		zap.Bool("is_onprem", cfg.Env.IsOnprem()))

	// 헬스체크 모드 처리 - health-check 플래그가 있으면 상태 확인 후 종료
	if *healthCheck {
		// TODO: 실제 헬스체크 로직 구현 필요
		// 현재는 설정 로드와 로거 초기화만 확인
		logger.Info("Health check passed")
		os.Exit(0)
	}

	// 공유 동시성 컨트롤러 생성 — HTTP/TCP 서버가 같은 슬롯 풀을 사용
	metrics := connection.NewMetricsCollector("git_executor")
	concurrencyConfig := connection.DefaultConcurrencyConfig()
	cc := connection.NewConcurrencyController(concurrencyConfig, metrics, logger.Logger)
	logger.Info("Shared concurrency controller initialized",
		zap.Int("max_concurrent_clones", concurrencyConfig.MaxConcurrentClones),
		zap.Int("max_concurrent_pushes", concurrencyConfig.MaxConcurrentPushes))

	// Git 서버 생성 - HTTP/SSH Git 프로토콜을 처리하는 서버 인스턴스 생성
	srv, err := server.New(cfg, cc)
	if err != nil {
		logger.Error("Failed to create server", zap.Error(err))
		os.Exit(1)
	}
	tcpSrv, err := server.NewDuplexTCPServer(cfg, cc)
	if err != nil {
		logger.Error("Failed to create TCP server", zap.Error(err))
		os.Exit(1)
	}

	// Start git maintenance scheduler
	maintenanceCfg := maintenance.DefaultMaintenanceConfig()
	maintenanceCfg.Enabled = cfg.Maintenance.Enabled
	maintenanceScheduler := maintenance.NewScheduler(maintenanceCfg, cfg.Git.RepoRoot, logger)
	maintenanceScheduler.Start()

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

	// Shutdown maintenance scheduler first (stop new maintenance jobs)
	maintenanceScheduler.Stop()

	// Shutdown both servers
	ctx := context.Background()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("Failed to shutdown HTTP server", zap.Error(err))
	}
	if err := tcpSrv.Stop(); err != nil {
		logger.Error("Failed to shutdown TCP server", zap.Error(err))
	}

	// 공유 동시성 컨트롤러를 마지막에 정리
	cc.Stop()

}

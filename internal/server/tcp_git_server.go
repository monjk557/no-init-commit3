package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/connection"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
	"go.uber.org/zap"
)

// DuplexTCPServer handles bidirectional TCP communication for Git operations
type DuplexTCPServer struct {
	config                *config.Config
	eventsClient          events.RedisStreamClient
	listener              net.Listener
	connections           map[net.Conn]*connection.ProgressAwareConnection
	concurrencyController *connection.ConcurrencyController
	timeoutPolicy         *connection.TimeoutPolicy
	metrics               *connection.MetricsCollector
	mu                    sync.RWMutex
	logger                *logging.Logger
}

// Legacy DuplexConnection type alias for compatibility during migration
type DuplexConnection = connection.ProgressAwareConnection

type v2SectionEnd int

const (
	v2SectionEndUnknown v2SectionEnd = iota
	v2SectionEndFlush                // 0000
	v2SectionEndDelim                // 0001
)

// deadlineRefreshingReader refreshes TCP read deadlines before each read.
// This makes large packfile pushes resilient by timing out on idle periods,
// not total transfer duration.
type deadlineRefreshingReader struct {
	r    io.Reader
	conn *net.TCPConn
	idle time.Duration
}

func (d *deadlineRefreshingReader) Read(p []byte) (int, error) {
	if d.conn != nil && d.idle > 0 {
		_ = d.conn.SetReadDeadline(time.Now().Add(d.idle))
	}
	return d.r.Read(p)
}

type sidebandWriter struct {
	w          io.Writer
	channel    byte // 1=pack, 2=progress, 3=fatal
	chunkSize  int  // payload(채널 포함) 최대 길이
	firstWrite bool // 첫 번째 쓰기인지 추적
}

// firstByteLogReader logs when the first byte of packfile is received (to distinguish client delay from server delay).
type firstByteLogReader struct {
	reader io.Reader
	logger interface{ Info(string, ...zap.Field) }
	start  time.Time
	once   sync.Once
}

func (r *firstByteLogReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.once.Do(func() {
		if n > 0 {
			r.logger.Info("First packfile data received from client",
				zap.Duration("wait_duration", time.Since(r.start)),
				zap.Int("first_chunk_bytes", n))
		}
	})
	return n, err
}

// NewDuplexTCPServer creates a new duplex TCP server with progress-aware connection management
func NewDuplexTCPServer(config *config.Config) (*DuplexTCPServer, error) {
	// Initialize logger
	logger, err := logging.NewLogger(config.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if config.Events.Enabled && config.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(config.Events.RedisURL, logger.Logger)
		if err != nil {
			logger.Warn("Failed to initialize Redis events client, continuing without events",
				zap.Error(err))
		}
	}

	// Initialize metrics collector
	metrics := connection.NewMetricsCollector("git_executor")

	// Initialize timeout policy
	timeoutPolicy := connection.DefaultTimeoutPolicy()

	// Initialize concurrency controller
	concurrencyConfig := connection.DefaultConcurrencyConfig()
	concurrencyController := connection.NewConcurrencyController(concurrencyConfig, metrics, logger.Logger)

	server := &DuplexTCPServer{
		config:                config,
		eventsClient:          eventsClient,
		connections:           make(map[net.Conn]*connection.ProgressAwareConnection),
		concurrencyController: concurrencyController,
		timeoutPolicy:         timeoutPolicy,
		metrics:               metrics,
		logger:                logger,
	}

	logger.Info("Progress-aware Git executor server initialized",
		zap.Int("max_concurrent_clones", concurrencyConfig.MaxConcurrentClones),
		zap.Int("max_concurrent_pushes", concurrencyConfig.MaxConcurrentPushes),
		zap.Duration("operation_idle_timeout", timeoutPolicy.OperationIdleTimeout),
		zap.Duration("operation_max_timeout", timeoutPolicy.OperationMaxTimeout))

	return server, nil
}

// Start starts the TCP server on configured port
func (s *DuplexTCPServer) Start() error {
	port := s.config.Server.TCPGitPort

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	s.listener = listener

	// Start TCP server in a goroutine
	go func() {
		s.logger.Info("TCP Git Server starting",
			zap.Int("port", port),
			zap.String("address", fmt.Sprintf(":%d", port)))

		// Start connection cleanup goroutine
		go s.cleanupConnections()

		// Accept connections
		for {
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Error("Failed to accept connection", zap.Error(err))
				continue
			}

			s.logger.Info("New TCP connection accepted",
				zap.String("remote_addr", conn.RemoteAddr().String()))
			go s.handleConnection(conn)
		}
	}()

	return nil
}

// Stop stops the TCP server and cleans up resources
func (s *DuplexTCPServer) Stop() error {
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}

	// Stop concurrency controller
	if s.concurrencyController != nil {
		s.concurrencyController.Stop()
	}

	// Close all active connections
	s.mu.Lock()
	for conn, progressConn := range s.connections {
		progressConn.Close()
		delete(s.connections, conn)
	}
	s.mu.Unlock()

	s.logger.Info("Progress-aware TCP server stopped")
	return err
}

// handleConnection : TCP 연결 처리 with progress-aware timeout management
func (s *DuplexTCPServer) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Create progress-aware connection wrapper
	progressConn := connection.NewProgressAwareConnection(conn, s.timeoutPolicy, s.logger.Logger)
	defer progressConn.Close()

	// Start with handshake phase
	if err := progressConn.SetPhase(connection.PhaseHandshake); err != nil {
		s.logger.Error("Failed to set handshake phase",
			zap.String("remote_addr", conn.RemoteAddr().String()),
			zap.Error(err))
		return
	}

	// Register connection
	s.mu.Lock()
	s.connections[conn] = progressConn
	s.mu.Unlock()

	// Cleanup on exit
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		s.logger.Debug("Connection cleaned up",
			zap.String("remote_addr", conn.RemoteAddr().String()))
	}()

	s.logger.Info("New progress-aware TCP connection established",
		zap.String("remote_addr", conn.RemoteAddr().String()))

	// Handle Git protocol negotiation
	if err := s.handleGitProtocol(progressConn); err != nil {
		s.logger.Error("Git protocol error",
			zap.String("remote_addr", conn.RemoteAddr().String()),
			zap.Error(err))
		s.metrics.RecordConnectionError("protocol_error", connection.OperationClone)
		return
	}
}

// handleGitProtocol : Git 프로토콜 핸드셰이크 처리 with phase management
func (s *DuplexTCPServer) handleGitProtocol(conn *connection.ProgressAwareConnection) error {
	// Transition to protocol negotiation phase
	if err := conn.SetPhase(connection.PhaseProtocolNegotiation); err != nil {
		return fmt.Errorf("failed to set protocol negotiation phase: %w", err)
	}

	// 1) 첫 pkt-line (service line) 읽기
	reader := conn.GetReader()
	line, isFlush, err := readPktLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read first pkt-line: %w", err)
	}
	if isFlush {
		return fmt.Errorf("unexpected flush before service line")
	}

	// 2) (선택) 추가 pkt-line 수집: 예) "version=2"
	var protoV2 bool
	for {
		l, flush, err := readPktLine(reader)
		if err != nil {
			return fmt.Errorf("failed to read subsequent pkt-line: %w", err)
		}
		if flush {
			break // 핸드셰이크 종료
		}
		if bytes.Equal(l, []byte("version=2")) {
			protoV2 = true
		}
	}

	// 3) service line 파싱: "<service> /owner/repo.git\0host=..\0user-id=xxx\0"
	// NUL(\x00) 기준으로 쪼갬
	parts0 := bytes.Split(line, []byte{0x00})
	if len(parts0) == 0 || len(parts0[0]) == 0 {
		return fmt.Errorf("invalid service line (empty)")
	}

	// Parse optional key=value tokens (e.g., host=..., user-id=...)
	for _, raw := range parts0[1:] {
		tok := strings.TrimSpace(string(raw))
		if tok == "" {
			continue
		}
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		var userID string
		switch k {
		case "user-id", "user_id", "userid", "uid":
			userID = v
		}
		// Set userID in connection metadata
		if userID != "" {
			owner, repo, _, protoV2Prev := conn.GetConnectionInfo()
			conn.SetConnectionInfo(owner, repo, userID, protoV2Prev)
		}
	}

	// 첫 토큰: "<service> /owner/repo.git"
	svcAndPath := parts0[0]
	sp := bytes.IndexByte(svcAndPath, ' ')
	if sp <= 0 || sp >= len(svcAndPath)-1 {
		return fmt.Errorf("invalid service+path format: %q", string(svcAndPath))
	}
	service := string(svcAndPath[:sp])
	repoPath := strings.TrimSpace(string(svcAndPath[sp+1:]))

	// repoPath 정규화: "/owner/repo.git" -> owner/repo
	repoPath = strings.Trim(repoPath, "'")
	repoPath = strings.TrimPrefix(repoPath, "/")
	pathParts := strings.Split(repoPath, "/")
	if len(pathParts) < 2 {
		return fmt.Errorf("invalid repository path: %s", repoPath)
	}
	owner := pathParts[0]
	repo := strings.TrimSuffix(pathParts[1], ".git")

	// Set connection metadata
	_, _, userID, _ := conn.GetConnectionInfo()
	conn.SetConnectionInfo(owner, repo, userID, protoV2)

	// 4) 서비스별 처리 (기존 로직 재사용)
	switch service {
	case "git-upload-pack":
		return s.handleUploadPack(conn, owner, repo)
	case "git-receive-pack":
		return s.handleReceivePack(conn, owner, repo)
	// case "git-upload-archive":
	// 	return s.handleUploadArchive(conn, owner, repo)
	default:
		return fmt.Errorf("unsupported Git service: %s", service)
	}
}

// handleUploadPack : git-upload-pack (fetch, clone) 처리 with progress-aware connection
func (s *DuplexTCPServer) handleUploadPack(conn *connection.ProgressAwareConnection, owner, repo string) error {
	// Transition to operation phase for clone/fetch
	if err := conn.SetPhase(connection.PhaseOperation); err != nil {
		return fmt.Errorf("failed to set operation phase: %w", err)
	}

	// Get client IP for concurrency control
	clientIP := connection.GetClientIP(conn.GetRemoteAddr())
	tenantID := fmt.Sprintf("%s/%s", owner, repo) // Use owner/repo as tenant ID

	// Request operation permission (clone)
	ctx := context.Background()
	if err := s.concurrencyController.RequestOperation(ctx, connection.OperationClone, tenantID, clientIP); err != nil {
		s.logger.Warn("Clone operation denied",
			zap.String("tenant", tenantID),
			zap.String("client_ip", clientIP),
			zap.Error(err))
		return fmt.Errorf("operation denied: %w", err)
	}

	// Ensure we release the operation slot
	operationStartTime := time.Now()
	defer func() {
		s.concurrencyController.ReleaseOperation(connection.OperationClone, tenantID, clientIP)
		s.metrics.RecordOperationComplete(connection.OperationClone, time.Since(operationStartTime), true, tenantID)
	}()

	// Get connection components
	reader := conn.GetReader()
	writer := conn.GetWriter()

	// 1) v2 광고 (ls-refs, fetch 지원)
	// Advertise shallow so clients can send "deepen N" (e.g. clone --depth 1).
	if err := protocol.WriteV2Advert(writer, "git-executor/0.1.0", "ls-refs", "fetch=shallow"); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush advert: %w", err)
	}

	// 2) 동기 상태머신 (한 섹션씩 처리)
	for {
		cmds, end, err := s.readSection(reader) // delim/flush까지 읽어들임
		if err != nil {
			return err
		}
		if len(cmds) == 0 {
			// Git protocol v2: empty-request = flush-pkt(0000)
			// Up-to-date 케이스에서는 ls-refs 이후 empty-request로 종료를 표시할 수 있음.
			// 이를 무시하고 계속 readSection을 호출하면 서버가 다음 섹션을 기다리다 30s 타임아웃으로 종료됨.
			if end == v2SectionEndFlush {
				return nil
			}
			continue
		}

		switch {
		// ---------------------------
		// ls-refs
		// ---------------------------
		case strings.HasPrefix(cmds[0], "command=ls-refs"):
			// [6] ls-refs 명령 수신
			// ls-refs options 섹션 읽기 (command 섹션 다음 섹션)
			// Git protocol v2에서 ls-refs는 두 섹션으로 구성:
			// 섹션 1: command=ls-refs + delimiter (0001)
			// 섹션 2: peel, symrefs, ref-prefix 등의 옵션들 + flush (0000)
			lsRefsOpts, _, err := s.readSection(reader)
			if err != nil {
				return fmt.Errorf("failed to read ls-refs options: %w", err)
			}
			// 두 섹션의 명령들을 합쳐서 파싱
			allCmds := append(cmds, lsRefsOpts...)
			lsRefsReq, err := protocol.ParseLsRefsSection(allCmds)
			if err != nil {
				return fmt.Errorf("failed to parse ls-refs section: %w", err)
			}
			// 참고: 내부에서 refs 응답 → 마지막에 flush("0000") + writer.Flush()까지 해야 함
			if err := s.handleLsRefs(conn, owner, repo, lsRefsReq); err != nil {
				return err
			}
			// [7] ls-refs 처리 완료
			continue

		// ---------------------------
		// fetch (clone/pull 공용)
		// ---------------------------
		case strings.HasPrefix(cmds[0], "command=fetch"):
			fetchCmds, _, err := s.readSection(reader)
			if err != nil {
				return fmt.Errorf("failed to read fetch options: %w", err)
			}

			fetchReq, err := protocol.ParseFetchSection(fetchCmds)
			if err != nil {
				return fmt.Errorf("failed to parse fetch section: %w", err)
			}
			if err := protocol.ValidateFetchSupport(fetchReq); err != nil {
				return fmt.Errorf("fetch not supported: %w", err)
			}

			// === 협상 라운드 (done 없음) ===
			// Git protocol v2 spec:
			// - haves 없음 (NAK): flush(0000) 보내고 다음 라운드(done 포함) 대기
			// - haves 있음 (ready): delim(0001) 보내고 바로 packfile 전송
			//   "ready"는 "공통 조상 찾았으니 packfile 바로 보내겠다"는 의미.
			//   delim 후 같은 스트림에서 packfile 섹션이 이어져야 한다.
			if !fetchReq.Done {
				if err := s.writePktLine(conn, "acknowledgments"); err != nil {
					return fmt.Errorf("failed to write acknowledgments header: %w", err)
				}
				if len(fetchReq.Haves) == 0 {
					// No common objects — send NAK + flush, wait for client to retry with done
					if err := s.writePktLine(conn, "NAK"); err != nil {
						return fmt.Errorf("failed to write NAK: %w", err)
					}
					if _, err := writer.WriteString("0000"); err != nil {
						return fmt.Errorf("failed to write flush-pkt: %w", err)
					}
					if err := conn.Flush(); err != nil {
						return fmt.Errorf("failed to flush after NAK: %w", err)
					}
					continue // 다음 라운드(done 포함)를 기다림
				}
				// Haves present — ACK common objects then send ready + delim, fall through to packfile
				repoPathForACK, ackPathErr := func() (string, error) {
					pc, pcErr := repositories.NewPathConfig(s.config.Git.RepoRoot)
					if pcErr != nil {
						return "", pcErr
					}
					return repositories.GetRepoPath(pc, owner, repo)
				}()
				if ackPathErr == nil {
					ackCtx := context.Background()
					for _, have := range fetchReq.Haves {
						if _, err := gitcmd.ObjectExists(ackCtx, repoPathForACK, have); err == nil {
							if err := s.writePktLine(conn, fmt.Sprintf("ACK %s", have)); err != nil {
								return fmt.Errorf("failed to write ACK: %w", err)
							}
						}
					}
				}
				if err := s.writePktLine(conn, "ready"); err != nil {
					return fmt.Errorf("failed to write ready: %w", err)
				}
				// Delimiter (0001): "다음 섹션(packfile)이 이어진다"
				if _, err := writer.WriteString("0001"); err != nil {
					return fmt.Errorf("failed to write delim-pkt: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush after ready: %w", err)
				}
				// Fall through to packfile transmission below
			}

			// === packfile 전송 ===
			pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
			if err != nil {
				return fmt.Errorf("failed to create path config: %w", err)
			}
			repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
			if err != nil {
				return fmt.Errorf("failed to get repository path: %w", err)
			}

			// Read lock to prevent concurrent writes during pack generation
			repoLock := repositories.GetRepoRWLock(repoPath)
			repoLock.RLock()
			defer repoLock.RUnlock()

			// Compute shallow graft commits for deepen requests.
			// These OIDs are sent in shallow-info AND passed as --shallow to pack-objects.
			var shallowGrafts []string
			if fetchReq.Deepen > 0 {
				shallowGrafts, err = protocol.GetShallowBoundary(repoPath, fetchReq)
				if err != nil {
					return fmt.Errorf("failed to get shallow boundary: %w", err)
				}
			}

			// Get object count for progress messages
			ctx := context.Background()
			totalObjects, countErr := gitcmd.CountObjects(ctx, repoPath, fetchReq.Wants, fetchReq.Haves, fetchReq.Deepen)
			if countErr != nil {
				totalObjects = 0 // Will still work, just no pre-progress
			}

			// === shallow-info section ===
			if len(shallowGrafts) > 0 {
				if err := s.writePktLine(conn, "shallow-info"); err != nil {
					return fmt.Errorf("failed to write shallow-info header: %w", err)
				}
				for _, h := range shallowGrafts {
					if err := s.writePktLine(conn, "shallow "+h); err != nil {
						return fmt.Errorf("failed to write shallow line: %w", err)
					}
				}
				if _, err := writer.WriteString("0001"); err != nil {
					return fmt.Errorf("failed to write shallow-info delim: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush after shallow-info: %w", err)
				}
			}

			// Merge shallow grafts into fetchReq.Shallow so pack-objects
			// receives --shallow <OID> lines and limits traversal correctly.
			if len(shallowGrafts) > 0 {
				fetchReq.Shallow = append(fetchReq.Shallow, shallowGrafts...)
			}

			// === packfile section ===
			if err := protocol.WritePackfileSectionHeader(writer); err != nil {
				return fmt.Errorf("failed to write packfile section header: %w", err)
			}
			if err := conn.Flush(); err != nil {
				return fmt.Errorf("failed to flush packfile section header: %w", err)
			}

			// sideband ch2: pre-pack progress
			if totalObjects > 0 {
				if err := writeProgressMessage(writer, fmt.Sprintf("Enumerating objects: %d, done.\n", totalObjects)); err != nil {
					return fmt.Errorf("failed to write enumerating message: %w", err)
				}
				if err := writeProgressMessage(writer, fmt.Sprintf("Counting objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
					return fmt.Errorf("failed to write counting completion: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush progress messages: %w", err)
				}
			}

			// sideband ch1: pack data via native git pack-objects (sole path, no go-git fallback)
			conn.GetProgressTracker().SetPackObjectsPhase(true)
			packCtx, packCancel := context.WithTimeout(context.Background(), 15*time.Minute)
			encErr := s.writePackfileViaGitPackObjects(packCtx, conn, repoPath, fetchReq)
			packCancel()
			conn.GetProgressTracker().SetPackObjectsPhase(false)
			if encErr != nil {
				return fmt.Errorf("failed to write packfile: %w", encErr)
			}

			// sideband ch2: post-pack progress
			if totalObjects > 0 {
				deltaCount := totalObjects / 8
				if err := writeProgressMessage(writer, fmt.Sprintf("Compressing objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
					return fmt.Errorf("failed to write compressing completion: %w", err)
				}
				summary := fmt.Sprintf("Total %d (delta %d), reused %d (delta %d), pack-reused 0 (from 0)\n",
					totalObjects, deltaCount, totalObjects, deltaCount)
				if err := writeProgressMessage(writer, summary); err != nil {
					return fmt.Errorf("failed to write summary: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush after packfile summary: %w", err)
				}
			}

			// Final flush to end packfile section + fetch response
			if _, err := writer.WriteString("0000"); err != nil {
				return fmt.Errorf("failed to write final flush: %w", err)
			}
			if err := conn.Flush(); err != nil {
				return fmt.Errorf("failed to flush writer: %w", err)
			}
			return nil
		}
	}
}

// handleReceivePack : git-receive-pack (push) 처리 with progress-aware connection and atomic updates
func (s *DuplexTCPServer) handleReceivePack(conn *connection.ProgressAwareConnection, owner, repo string) error {
	// Transition to operation phase for push
	if err := conn.SetPhase(connection.PhaseOperation); err != nil {
		return fmt.Errorf("failed to set operation phase: %w", err)
	}

	// Get client IP for concurrency control
	clientIP := connection.GetClientIP(conn.GetRemoteAddr())
	tenantID := fmt.Sprintf("%s/%s", owner, repo) // Use owner/repo as tenant ID

	// Request operation permission (push)
	ctx := context.Background()
	if err := s.concurrencyController.RequestOperation(ctx, connection.OperationPush, tenantID, clientIP); err != nil {
		s.logger.Warn("Push operation denied",
			zap.String("tenant", tenantID),
			zap.String("client_ip", clientIP),
			zap.Error(err))
		return fmt.Errorf("operation denied: %w", err)
	}

	// Ensure we release the operation slot
	operationStartTime := time.Now()
	var operationSuccess bool
	defer func() {
		s.concurrencyController.ReleaseOperation(connection.OperationPush, tenantID, clientIP)
		s.metrics.RecordOperationComplete(connection.OperationPush, time.Since(operationStartTime), operationSuccess, tenantID)
	}()

	// 1) Repository 열기
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repo path: %w", err)
	}

	// Validate repository exists (consistent with HTTP/SSH handlers).
	// Without this, index-pack may fail with "Bad file descriptor" if
	// the repo structure is incomplete or missing.
	if err := repositories.EnsureRepoExists(pathConfig, repoPath); err != nil {
		return fmt.Errorf("repository not found: %w", err)
	}

	// Serialize pack/idx writes (push) vs reads (overview) per repo to avoid transient corruption.
	repoLock := repositories.GetRepoRWLock(repoPath)
	s.logger.Info("repo_write_lock_acquire", zap.String("repo_path", repoPath))
	repoLock.Lock()
	s.logger.Info("repo_write_lock_acquired", zap.String("repo_path", repoPath))
	defer func() {
		s.logger.Info("repo_write_lock_release", zap.String("repo_path", repoPath))
		repoLock.Unlock()
	}()

	s.logger.Info("Opening repository", zap.String("repo_path", repoPath))

	// Initialize atomic push handler using native git
	operationID := fmt.Sprintf("push-%s-%s-%d", owner, repo, time.Now().UnixNano())
	atomicPushHandler := connection.NewAtomicPushHandler(repoPath, operationID, s.logger.Logger)

	// Get connection components
	reader := conn.GetReader()
	writer := conn.GetWriter()

	// 2) V1 advertisement using native git
	caps := []string{"report-status", "delete-refs", "thin-pack", "atomic"}
	if err := protocol.WriteV1Advert(writer, repoPath, caps...); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush advert: %w", err)
	}

	// 3) ref updates 읽기 (v1 프로토콜: pkt-line 형식)
	var refUpdates []string

	s.logger.Info("Reading ref updates section")
	// v1 receive-pack에서는 pkt-line 형식으로 ref update 명령들이 오고 flush(0000)로 끝남
	// 클라이언트가 광고를 받은 후 ref updates를 보내기까지 시간이 걸릴 수 있으므로
	// 첫 번째 읽기에서 flush가 오면 잠시 대기 후 다시 시도
	firstRead := true
	for {
		line, isFlush, err := readPktLine(reader)
		if err != nil {
			// 타임아웃이나 연결 종료 에러인 경우
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				s.logger.Warn("Read timeout while waiting for ref updates, assuming no updates")
				break
			}
			// 클라이언트가 조기 종료한 경우(EOF)는 ref updates가 없는 것으로 처리
			if errors.Is(err, io.EOF) {
				s.logger.Warn("Client closed connection while waiting for ref updates; treating as no updates")
				break
			}
			return fmt.Errorf("failed to read ref update: %w", err)
		}
		if isFlush {
			if firstRead {
				// Everything-up-to-date 케이스: 클라이언트가 ref updates 없이 flush만 보내고 종료할 수 있음.
				// 이 경우 재시도하면 net.ErrClosed로 터질 수 있으니 그대로 "updates 없음"으로 종료한다.
				s.logger.Info("Received flush immediately after advertisement; treating as no ref updates")
				break
			}
			// flush packet으로 ref updates 섹션 종료
			s.logger.Info("Received flush packet, ending ref updates section")
			break
		}
		firstRead = false
		if len(line) > 0 {
			s.logger.Debug("Received ref update", zap.String("update", string(line)))
			refUpdates = append(refUpdates, string(line))
		}
	}
	s.logger.Info("Ref updates read", zap.Int("count", len(refUpdates)), zap.Strings("updates", refUpdates))

	// Everything up-to-date: client may send only a flush (no commands, no packfile)
	// and wait for the server to finish. Do NOT block on packfile Peek().
	if len(refUpdates) == 0 {
		s.logger.Info("No ref updates received; completing receive-pack early")
		return nil
	}

	// Prepare atomic push operation with collected ref updates
	if len(refUpdates) > 0 {
		if err := atomicPushHandler.PreparePushOperation(refUpdates); err != nil {
			s.logger.Error("Failed to prepare atomic push operation", zap.Error(err))
			return fmt.Errorf("failed to prepare push operation: %w", err)
		}
	}

	// Packfile processing using native git index-pack --stdin --fix-thin
	hasPackfile := false
	if peekData, err := reader.Peek(4); err == nil && len(peekData) == 4 {
		if string(peekData) == "PACK" {
			hasPackfile = true
		}
	}

	if hasPackfile {
		s.logger.Info("Starting packfile read with progress tracking")

		progressTracker := conn.GetProgressTracker()
		progressTracker.SetPackObjectsPhase(true)
		defer progressTracker.SetPackObjectsPhase(false)

		packfileReadStart := time.Now()

		// Use native git index-pack to process the incoming packfile stream.
		// --fix-thin automatically resolves thin pack deltas using repo objects.
		// This replaces ~190 lines of go-git packfile parsing + thin-pack fallback.
		indexCtx, indexCancel := context.WithTimeout(context.Background(), 15*time.Minute)
		_, indexErr := gitcmd.IndexPackFromReader(indexCtx, repoPath, reader)
		indexCancel()

		if indexErr != nil {
			s.logger.Error("Failed to process packfile via git index-pack", zap.Error(indexErr))
			if err := atomicPushHandler.HandlePushTimeout(); err != nil {
				s.logger.Error("Failed to handle push timeout", zap.Error(err))
			}
			// V1 report-status: unpack-status + 1*(command-status) + flush.
			// Without ng lines, clients report "failed to report status".
			_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", indexErr.Error()))
			s.writeNgForAllRefs(conn, refUpdates, "unpack failed")
			_, _ = writer.WriteString("0000")
			_ = conn.Flush()
			return indexErr
		}

		s.logger.Info("Successfully unpacked packfile via native git index-pack",
			zap.Duration("duration", time.Since(packfileReadStart)))
	} else {
		s.logger.Info("No packfile detected, skipping unpack")
	}

	// 7) Execute atomic push operation
	if len(refUpdates) > 0 {
		s.logger.Info("Executing atomic push operation", zap.String("operation_id", operationID))
		if err := atomicPushHandler.ExecutePushOperation(); err != nil {
			s.logger.Error("Failed to execute atomic push operation", zap.Error(err))
			// V1 report-status: unpack-status + 1*(command-status) + flush.
			_ = s.writePktLine(conn, fmt.Sprintf("unpack atomic push failed: %s", err.Error()))
			s.writeNgForAllRefs(conn, refUpdates, "atomic push failed")
			if _, wErr := writer.WriteString("0000"); wErr != nil {
				return fmt.Errorf("failed to write flush: %w", wErr)
			}
			if fErr := conn.Flush(); fErr != nil {
				return fmt.Errorf("failed to flush: %w", fErr)
			}
			return err
		}
		s.logger.Info("Atomic push operation executed", zap.String("operation_id", operationID))
	}

	// Invalidate repository cache so overview/read paths re-open and see new pack/idx + refs.
	removed := repositories.GetSimpleCache().Invalidate(repoPath)
	s.logger.Info("Repository cache invalidated after push",
		zap.String("repo_path", repoPath),
		zap.Bool("was_cached", removed))

	// Post-push readiness check: reopen and read the pushed commit until success (async; does not delay client).
	// This helps pinpoint when the repo becomes readable for overview right after push.
	if len(refUpdates) > 0 {
		refUpdate := refUpdates[0]
		var newHash, refName string
		if strings.Contains(refUpdate, "\x00") {
			parts := strings.SplitN(refUpdate, "\x00", 2)
			hashParts := strings.Fields(parts[0])
			if len(hashParts) >= 3 {
				newHash, refName = hashParts[1], hashParts[2]
			}
		} else {
			hashParts := strings.Fields(refUpdate)
			if len(hashParts) >= 3 {
				newHash, refName = hashParts[1], hashParts[2]
			}
		}
		if newHash != "" && refName != "" {
			go func(repoPath, newHash, refName, operationID string) {
				start := time.Now()
				deadline := time.Now().Add(3 * time.Second)
				attempt := 0
				for time.Now().Before(deadline) {
					attempt++
					ctx := context.Background()
					_, err := gitcmd.ObjectExists(ctx, repoPath, newHash)
					if err == nil {
						s.logger.Info("Repository readable after push",
							zap.String("operation_id", operationID),
							zap.String("ref", refName),
							zap.String("commit", newHash),
							zap.Int("attempt", attempt),
							zap.Duration("duration", time.Since(start)))
						return
					}
					s.logger.Debug("Repository not yet readable after push",
						zap.String("operation_id", operationID),
						zap.String("ref", refName),
						zap.String("commit", newHash),
						zap.Int("attempt", attempt),
						zap.Error(err))
					time.Sleep(100 * time.Millisecond)
				}
				s.logger.Warn("Repository still not readable after push (timeout)",
					zap.String("operation_id", operationID),
					zap.String("ref", refName),
					zap.String("commit", newHash),
					zap.Int("attempts", attempt),
					zap.Duration("duration", time.Since(start)))
			}(repoPath, newHash, refName, operationID)
		}
	}

	// 8) unpack 성공 응답 전송
	if err := s.writePktLine(conn, "unpack ok"); err != nil {
		return fmt.Errorf("failed to write unpack ok: %w", err)
	}

	// 9) 각 ref 업데이트 성공 응답 전송
	if len(refUpdates) == 0 {
		s.logger.Warn("No ref updates received from client - this may indicate a protocol issue or empty push")
		// ref updates가 없으면 "unpack ok"만 보내고 종료
		// (클라이언트가 push할 내용이 없으면 ref updates가 없을 수 있음)
	} else {
		// Since we used atomic push, all refs were updated successfully or none were updated
		// Send success response for each ref that was processed atomically
		_, _, userID, _ := conn.GetConnectionInfo()
		for _, refUpdate := range refUpdates {
			// Parse ref update line for response formatting
			var refName string

			if strings.Contains(refUpdate, "\x00") {
				// Null-separated format: "old-hash new-hash ref-name\0capabilities"
				parts := strings.SplitN(refUpdate, "\x00", 2)
				refAndHashes := parts[0]
				hashParts := strings.Fields(refAndHashes)
				if len(hashParts) >= 3 {
					refName = hashParts[2]
				}
			} else {
				// Space-separated format: "old-hash new-hash ref-name [capabilities]"
				parts := strings.Fields(refUpdate)
				if len(parts) >= 3 {
					refName = parts[2]
				}
			}

			if refName == "" {
				s.logger.Warn("Invalid ref update format for response", zap.String("update", refUpdate))
				continue
			}

			s.logger.Info("Ref update processed atomically",
				zap.String("ref_name", refName),
				zap.String("operation_id", operationID))

			// Send success response for this ref (all succeeded due to atomic operation)
			statusLine := fmt.Sprintf("ok %s", refName)
			if err := s.writePktLine(conn, statusLine); err != nil {
				return fmt.Errorf("failed to write ref success status: %w", err)
			}

			// Handle events for successful ref updates
			if s.eventsClient != nil && strings.HasPrefix(refName, "refs/heads/") {
				// Extract old and new hashes for event
				var oldHash, newHash string
				if strings.Contains(refUpdate, "\x00") {
					parts := strings.SplitN(refUpdate, "\x00", 2)
					hashParts := strings.Fields(parts[0])
					if len(hashParts) >= 2 {
						oldHash, newHash = hashParts[0], hashParts[1]
					}
				} else {
					parts := strings.Fields(refUpdate)
					if len(parts) >= 2 {
						oldHash, newHash = parts[0], parts[1]
					}
				}

				if oldHash != "" && newHash != "" {
					s.addCommitEventToStream(repoPath, oldHash, newHash, refName, owner, repo, userID, events.EventTypePush)
				}
			}
		}
	}

	// 10) 최종 flush 패킷 전송
	if _, err := writer.WriteString("0000"); err != nil {
		return fmt.Errorf("failed to write final flush: %w", err)
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("failed to flush writer: %w", err)
	}

	// Mark operation as successful
	operationSuccess = true
	s.logger.Info("Atomic push request processed successfully",
		zap.String("operation_id", operationID),
		zap.Int("ref_count", len(refUpdates)),
		zap.Duration("duration", time.Since(operationStartTime)))
	return nil
}

// updateRef is no longer needed — all ref updates are handled atomically
// via gitcmd.AtomicRefUpdate in AtomicPushHandler.ExecutePushOperation.

// handleLsRefs : ls-refs 명령 처리 (info/refs) with progress-aware connection
// Uses native git commands via gitcmd package.
func (s *DuplexTCPServer) handleLsRefs(conn *connection.ProgressAwareConnection, owner, repo string, lsRefsReq *protocol.LsRefsReq) error {
	writer := conn.GetWriter()

	// Create path config and get repository path
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	repoPath, err := repositories.GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repository path: %w", err)
	}

	ctx := context.Background()

	// Determine ref patterns to list
	patterns := lsRefsReq.RefPrefix
	if len(patterns) == 0 {
		patterns = []string{"refs/heads/", "refs/tags/"}
	}

	// Check if HEAD is requested
	headRequested := false
	if len(lsRefsReq.RefPrefix) > 0 {
		for _, prefix := range lsRefsReq.RefPrefix {
			if prefix == "HEAD" || strings.HasPrefix("HEAD", prefix) {
				headRequested = true
				break
			}
		}
	} else {
		headRequested = true
	}

	// Handle HEAD using native git
	if headRequested {
		headInfo, err := gitcmd.ResolveHEAD(ctx, repoPath)
		if err == nil {
			if headInfo.IsSymbolic && lsRefsReq.Symrefs {
				refLine := fmt.Sprintf("%s HEAD symref-target:%s", headInfo.OID, headInfo.Target)
				if err := s.writePktLine(conn, refLine); err != nil {
					return fmt.Errorf("failed to write HEAD symref: %w", err)
				}
			} else if headInfo.OID != "" {
				refLine := fmt.Sprintf("%s HEAD", headInfo.OID)
				if err := s.writePktLine(conn, refLine); err != nil {
					return fmt.Errorf("failed to write HEAD hash: %w", err)
				}
			}
		}
		// HEAD not found is not an error — continue listing refs
	}

	// List refs using native git for-each-ref
	refs, err := gitcmd.ListRefs(ctx, repoPath, patterns...)
	if err != nil {
		// Treat as empty repo
		pw := &protocol.PktWriter{W: writer, WithLF: false}
		return pw.Flush()
	}

	for _, ref := range refs {
		refName := ref.RefName

		// Skip HEAD (already handled above)
		if refName == "HEAD" {
			continue
		}

		// Apply ref-prefix filtering if specified
		if len(lsRefsReq.RefPrefix) > 0 {
			matched := false
			for _, prefix := range lsRefsReq.RefPrefix {
				if strings.HasPrefix(refName, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Handle peel for tags
		if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") && ref.PeeledOID != "" {
			// Send tag hash
			refLine := fmt.Sprintf("%s %s", ref.ObjectName, refName)
			if err := s.writePktLine(conn, refLine); err != nil {
				return err
			}
			// Send peeled hash
			peeledLine := fmt.Sprintf("peeled %s", ref.PeeledOID)
			if err := s.writePktLine(conn, peeledLine); err != nil {
				return err
			}
			continue
		}

		// Handle symbolic refs
		if ref.SymRef != "" && lsRefsReq.Symrefs {
			refLine := fmt.Sprintf("@%s %s", ref.SymRef, refName)
			if err := s.writePktLine(conn, refLine); err != nil {
				return err
			}
			continue
		}

		// Regular hash reference
		refLine := fmt.Sprintf("%s %s", ref.ObjectName, refName)
		if err := s.writePktLine(conn, refLine); err != nil {
			return err
		}
	}

	pw := &protocol.PktWriter{W: writer, WithLF: false}
	if err := pw.Flush(); err != nil {
		return fmt.Errorf("failed to write flush packet: %w", err)
	}

	return conn.Flush()
}

func newSidebandWriter(w io.Writer, channel byte) *sidebandWriter {
	// pkt-line 총 길이(4바이트 헤더 포함) 최대 65520.
	// payload = length-4, 그 중 1바이트는 채널이 차지.
	// 안전하게 payload(채널 포함) 65515 사용.
	return &sidebandWriter{w: w, channel: channel, chunkSize: 65515, firstWrite: true}
}

func (sb *sidebandWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	total := 0
	for len(p) > 0 {
		n := sb.chunkSize - 1 // 채널 바이트(1)를 뺀 실제 데이터 공간
		if n > len(p) {
			n = len(p)
		}
		chunk := p[:n]
		p = p[n:]

		payloadLen := 1 + len(chunk) // [channel][data...]
		length := payloadLen + 4     // pkt-line 총 길이
		if _, err := fmt.Fprintf(sb.w, "%04x", length); err != nil {
			return total, err
		}
		if _, err := sb.w.Write([]byte{sb.channel}); err != nil {
			return total, err
		}
		if _, err := sb.w.Write(chunk); err != nil {
			return total, err
		}

		// 첫 번째 chunk를 즉시 전송하도록 flush
		// "PACK" 헤더를 포함한 첫 번째 패킷에 중요합니다
		// "packfile" 섹션 헤더 이후, 클라이언트는 즉시 sideband 데이터를 기대합니다
		if sb.firstWrite {
			// [17-3-1] 첫 chunk 전송, flush
			// 기본 writer가 지원하는 경우 flush 시도
			if flusher, ok := sb.w.(interface{ Flush() error }); ok {
				if err := flusher.Flush(); err != nil {
					return total, err
				}
			}
			sb.firstWrite = false
		}

		total += len(chunk)
	}
	return total, nil
}

func (sb *sidebandWriter) Flush() error {
	_, err := sb.w.Write([]byte("0000")) // sideband 스트림 종료
	return err
}

// writeProgressMessage는 sideband channel 2 (progress)로 progress 메시지를 전송합니다
func writeProgressMessage(w io.Writer, message string) error {
	// Progress 메시지는 channel 2 (0x02)를 사용합니다
	// 형식: [4-byte hex length][0x02][message]
	messageBytes := []byte(message)
	payloadLen := 1 + len(messageBytes) // channel byte + message
	length := payloadLen + 4            // 전체 pkt-line 길이

	if _, err := fmt.Fprintf(w, "%04x", length); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0x02}); err != nil { // channel 2 = progress
		return err
	}
	if _, err := w.Write(messageBytes); err != nil {
		return err
	}
	return nil
}

// writePackfileWithSideband is removed — all packfile encoding is now done via
// native git pack-objects (writePackfileViaGitPackObjects).

// getFetchObjectCount returns the number of commits to send using gitcmd.CountObjects.
func (s *DuplexTCPServer) getFetchObjectCount(repoPath string, fetchReq *protocol.FetchReq) (int, error) {
	ctx := context.Background()
	return gitcmd.CountObjects(ctx, repoPath, fetchReq.Wants, fetchReq.Haves, fetchReq.Deepen)
}

// writePackfileViaGitPackObjects runs native git pack-objects via gitcmd.RunPackObjects,
// streaming stdout into sideband (channel 0x01).
func (s *DuplexTCPServer) writePackfileViaGitPackObjects(
	ctx context.Context,
	conn *connection.ProgressAwareConnection,
	repoPath string,
	fetchReq *protocol.FetchReq,
) error {
	if len(fetchReq.Wants) == 0 {
		return fmt.Errorf("no wants")
	}

	sbw := newSidebandWriter(conn.GetWriter(), 0x01)

	opts := &gitcmd.PackObjectsOpts{
		Wants:         fetchReq.Wants,
		Haves:         fetchReq.Haves,
		ThinPack:      fetchReq.ThinPack,
		OfsDelta:      fetchReq.OfsDelta,
		IncludeTag:    fetchReq.IncludeTag,
		Deepen:        fetchReq.Deepen,
		Shallow:       fetchReq.Shallow,
		Filter:        fetchReq.Filter,
		DeepenSince:   fetchReq.DeepenSince,
		DeepenExclude: fetchReq.DeepenNot,
	}

	_, err := gitcmd.RunPackObjects(ctx, repoPath, opts, sbw)
	if err != nil {
		return fmt.Errorf("git pack-objects: %w", err)
	}

	// 섹션 종료(0000)는 호출자가 progress 후 전송.
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush after pack-objects: %w", err)
	}
	return nil
}

// readV2Section : v2 섹션 읽기 (delimiter/flush 전까지)
func (s *DuplexTCPServer) readSection(reader *bufio.Reader) ([]string, v2SectionEnd, error) {
	var commands []string
	end := v2SectionEndUnknown

	for {
		// 1) 4바이트 길이 헤더 (hex)
		hdr := make([]byte, 4)

		if _, err := io.ReadFull(reader, hdr); err != nil {
			return nil, end, fmt.Errorf("read len hdr: %w", err)
		}

		// 2) flush 패킷인지 확인
		if string(hdr) == "0000" {
			// 섹션 끝 → 루프 탈출
			end = v2SectionEndFlush
			break
		}

		// 3) 길이 파싱
		n64, err := strconv.ParseUint(string(hdr), 16, 16)
		if err != nil {
			return nil, end, fmt.Errorf("bad pkt-len %q: %w", string(hdr), err)
		}
		n := int(n64)

		// 0001은 delimiter 패킷 (capabilities와 데이터 구분)
		// delimiter를 만나면 현재 섹션을 종료하고 반환
		if n == 1 {
			end = v2SectionEndDelim
			break
		}

		if n < 4 {
			return nil, end, fmt.Errorf("invalid pkt-len: %d", n)
		}

		// 4) payload 정확히 n-4바이트 읽기
		payload := make([]byte, n-4)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, end, fmt.Errorf("read payload: %w", err)
		}

		// 5) 명령 추가 (개행문자 포함될 수 있으므로 trim)
		cmdStr := strings.TrimSpace(string(payload))
		if cmdStr != "" {
			commands = append(commands, cmdStr)
		}
	}
	return commands, end, nil
}

// readPktLine: pkt-line 한 프레임을 읽는다.
// 반환: payload, isFlush(0000), error
func readPktLine(r *bufio.Reader) ([]byte, bool, error) {
	// 4바이트 길이 헤더 (hex)
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, false, err
	}
	if bytes.Equal(hdr, []byte("0000")) {
		return nil, true, nil // flush
	}
	n, err := strconv.ParseUint(string(hdr), 16, 16)
	if err != nil {
		return nil, false, fmt.Errorf("bad pkt-len: %q", string(hdr))
	}
	if n < 4 {
		return nil, false, fmt.Errorf("invalid pkt-len: %d", n)
	}
	payloadLen := int(n) - 4
	buf := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, false, err
	}
	return buf, false, nil
}

// writePktLine : pkt-line 전송 with progress-aware connection
// Payload includes trailing LF per Git protocol spec (both v1 and v2).
func (s *DuplexTCPServer) writePktLine(conn *connection.ProgressAwareConnection, data string) error {
	writer := conn.GetWriter()
	// pkt-line format: [4-hex-len][data]\n — length includes the 4-byte header + data + LF
	length := len(data) + 5 // 4 (header) + len(data) + 1 (LF)
	line := fmt.Sprintf("%04x%s\n", length, data)
	_, err := writer.WriteString(line)
	return err
}

// writeNgForAllRefs sends "ng <ref> <reason>" for every ref update command.
// Required by V1 report-status protocol: the response must include at least
// one command-status line after the unpack-status line. Without this, the
// client reports "remote failed to report status".
func (s *DuplexTCPServer) writeNgForAllRefs(conn *connection.ProgressAwareConnection, refUpdates []string, reason string) {
	for _, refUpdate := range refUpdates {
		var refName string
		if strings.Contains(refUpdate, "\x00") {
			parts := strings.SplitN(refUpdate, "\x00", 2)
			hashParts := strings.Fields(parts[0])
			if len(hashParts) >= 3 {
				refName = hashParts[2]
			}
		} else {
			parts := strings.Fields(refUpdate)
			if len(parts) >= 3 {
				refName = parts[2]
			}
		}
		if refName != "" {
			_ = s.writePktLine(conn, fmt.Sprintf("ng %s %s", refName, reason))
		}
	}
}

// cleanupConnections : 유휴 연결 정리 with progress-aware monitoring
func (s *DuplexTCPServer) cleanupConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		for conn, progressConn := range s.connections {
			// Use IsAlive to check if connection is healthy and not stalled
			if !progressConn.IsAlive() {
				s.logger.Debug("Cleaning up unhealthy connection",
					zap.String("remote_addr", conn.RemoteAddr().String()))
				progressConn.Close()
				delete(s.connections, conn)
			}
		}
		s.mu.Unlock()
	}
}

// addCommitEventToStream adds a commit event to Redis Stream using native git.
func (s *DuplexTCPServer) addCommitEventToStream(repoPath, oldHash, commitHash, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if s.eventsClient == nil {
		return
	}

	ctx := context.Background()

	// Get commit info using native git cat-file
	commitInfo, err := gitcmd.GetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		s.logger.Debug("Skipping event publish; commit not found",
			zap.String("commit_hash", commitHash),
			zap.Error(err))
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, s.config.Git.RepoRoot)

	// Create event
	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     eventType,
		CommitHash:    commitHash,
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: commitInfo.Message,
		Timestamp:     commitInfo.CommitterDate,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.eventsClient.AddCommitEventToStream(pubCtx, event); err != nil {
			s.logger.Warn("Failed to publish commit event",
				zap.String("repo_path", repoPath),
				zap.String("commit_hash", commitHash),
				zap.Error(err))
		}
	}()
}

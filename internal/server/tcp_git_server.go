package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/connection"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
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
			// NAK: 클라이언트가 done 포함 fetch를 재전송할 때까지 대기.
			// ready: 클라이언트(Apple Git 등)는 ready 직후 곧바로 "packfile" 섹션 시작을 기대함(수 ms 이내).
			// 0000 직후 즉시 "packfile" pkt-line을 보내지 않으면 클라이언트가 "expected packfile to be sent after 'ready'"로 종료함.
			packfileHeaderSent := false
			if !fetchReq.Done {
				if err := s.writePktLine(conn, "acknowledgments"); err != nil {
					return fmt.Errorf("failed to write acknowledgments header: %w", err)
				}
				if len(fetchReq.Haves) == 0 {
					if err := s.writePktLine(conn, "NAK"); err != nil {
						return fmt.Errorf("failed to write NAK: %w", err)
					}
					// flush-pkt(0000): 클라이언트가 done 포함 fetch 재전송할 때까지 대기.
					if _, err := writer.WriteString("0000"); err != nil {
						return fmt.Errorf("failed to write flush-pkt: %w", err)
					}
					if err := conn.Flush(); err != nil {
						return fmt.Errorf("failed to flush after acknowledgments: %w", err)
					}
					continue
				}
				if err := s.writePktLine(conn, "ready"); err != nil {
					return fmt.Errorf("failed to write ready: %w", err)
				}
				if _, err := writer.WriteString("0000"); err != nil {
					return fmt.Errorf("failed to write flush-pkt: %w", err)
				}
				// 스펙: packfile = PKT-LINE("packfile" LF). ready 직후 0000만 보내면 클라이언트가
				// "expected packfile to be sent after 'ready'"로 종료하므로, 같은 writePktLine 경로로
				// "packfile\n"을 쓴 뒤 한 번만 Flush (0000과 packfile을 한 번에 전송).
				if err := s.writePktLine(conn, "packfile\n"); err != nil {
					return fmt.Errorf("failed to write packfile section header after ready: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush after acknowledgments and packfile header: %w", err)
				}
				packfileHeaderSent = true
			}

			// === packfile 전송 라운드 (done 수신 또는 ready 직후) ===
			pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
			if err != nil {
				return fmt.Errorf("failed to create path config: %w", err)
			}
			repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
			if err != nil {
				return fmt.Errorf("failed to get repository path: %w", err)
			}

			// Shared ObjectStorage 접근 전에 read lock 획득.
			// commit/push/merge 가 Lock() 으로 pack 쓰는 동안 concurrent read 방지.
			repoLock := repositories.GetRepoRWLock(repoPath)
			repoLock.RLock()
			defer repoLock.RUnlock()

			repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
			if err != nil {
				pw := &protocol.PktWriter{W: writer, WithLF: false}
				if err := pw.Err("repository not found"); err != nil {
					return fmt.Errorf("failed to write error response: %w", err)
				}
				_ = conn.Flush()
				return nil
			}
			defer func() { _ = repoWrapper.Close() }()

			underlyingRepo := repoWrapper.GetRepo()

			// Light path: get shallow boundary and object count without full go-git enumeration.
			// BuildFetchObjectList is only used when count fails or when pack-objects fails (fallback).
			var shallowBoundary []plumbing.Hash
			if fetchReq.Deepen > 0 {
				shallowBoundary, err = protocol.GetShallowBoundary(underlyingRepo, fetchReq)
				if err != nil {
					return fmt.Errorf("failed to get shallow boundary: %w", err)
				}
			}
			totalObjects, countErr := s.getFetchObjectCount(repoPath, fetchReq)
			var objectHashes []plumbing.Hash
			if countErr != nil {
				// Fallback: need full list for progress and for writePackfileWithSideband.
				var fetchResult *protocol.BuildFetchObjectListResult
				fetchResult, err = protocol.BuildFetchObjectList(underlyingRepo.Storer, underlyingRepo, fetchReq)
				if err != nil {
					return fmt.Errorf("failed to build fetch object list: %w", err)
				}
				objectHashes = fetchResult.Objects
				shallowBoundary = fetchResult.ShallowBoundary
				totalObjects = len(objectHashes)
			}

			// done=true: acknowledgments 섹션 생략 (스펙 §6.4)

			// === shallow-info 섹션 (deepen N 사용 시) ===
			if len(shallowBoundary) > 0 {
				if err := s.writePktLine(conn, "shallow-info"); err != nil {
					return fmt.Errorf("failed to write shallow-info header: %w", err)
				}
				for _, h := range shallowBoundary {
					if err := s.writePktLine(conn, "shallow "+h.String()); err != nil {
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

			// === packfile 섹션 ===
			if totalObjects > 0 {
				if !packfileHeaderSent {
					if err := protocol.WritePackfileSectionHeader(writer); err != nil {
						return fmt.Errorf("failed to write packfile section header: %w", err)
					}
					if err := conn.Flush(); err != nil {
						return fmt.Errorf("failed to flush packfile section header: %w", err)
					}
				}

				// sideband ch2: pre-pack progress
				if err := writeProgressMessage(writer, fmt.Sprintf("Enumerating objects: %d, done.\n", totalObjects)); err != nil {
					return fmt.Errorf("failed to write enumerating message: %w", err)
				}
				if err := writeProgressMessage(writer, fmt.Sprintf("Counting objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
					return fmt.Errorf("failed to write counting completion: %w", err)
				}
				if err := conn.Flush(); err != nil {
					return fmt.Errorf("failed to flush progress messages: %w", err)
				}

				// sideband ch1: pack data. git pack-objects 우선(순차 읽기로 수십 초 단축), 실패 시 go-git 인코더 폴백.
				conn.GetProgressTracker().SetPackObjectsPhase(true)
				packCtx, packCancel := context.WithTimeout(context.Background(), 15*time.Minute)
				encErr := s.writePackfileViaGitPackObjects(packCtx, conn, repoPath, fetchReq)
				packCancel()
				if encErr != nil {
					// Fallback: need full object list from go-git (only when pack-objects failed).
					if len(objectHashes) == 0 {
						var fetchResult *protocol.BuildFetchObjectListResult
						fetchResult, err = protocol.BuildFetchObjectList(underlyingRepo.Storer, underlyingRepo, fetchReq)
						if err != nil {
							return fmt.Errorf("failed to build fetch object list: %w", err)
						}
						objectHashes = fetchResult.Objects
					}
					encErr = s.writePackfileWithSideband(conn, underlyingRepo.Storer, objectHashes, fetchReq)
				}
				conn.GetProgressTracker().SetPackObjectsPhase(false)
				if encErr != nil {
					return fmt.Errorf("failed to write packfile: %w", encErr)
				}

				// sideband ch2: post-pack progress
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

			// 0000 하나로 packfile 섹션 + fetch 응답 전체 종료
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
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		s.logger.Error("Failed to open repository",
			zap.String("repo_path", repoPath),
			zap.Error(err))
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Initialize atomic push handler
	operationID := fmt.Sprintf("push-%s-%s-%d", owner, repo, time.Now().UnixNano())
	atomicPushHandler := connection.NewAtomicPushHandler(gitRepo, operationID, s.logger.Logger)

	// Get connection components
	reader := conn.GetReader()
	writer := conn.GetWriter()

	// 2) V1 광고 전송
	if err := protocol.WriteV1Advert(writer, *gitRepo); err != nil {
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

	// 4) 실제 ref 업데이트 수행
	workTree, err := gitRepo.Worktree()
	if err != nil {
		s.logger.Info("Repository is bare (no worktree)", zap.String("repo_path", repoPath))
	} else {
		s.logger.Info("Repository has worktree",
			zap.String("repo_path", repoPath),
			zap.String("worktree_path", workTree.Filesystem.Root()))
	}

	// Prepare atomic push operation with collected ref updates
	if len(refUpdates) > 0 {
		if err := atomicPushHandler.PreparePushOperation(refUpdates); err != nil {
			s.logger.Error("Failed to prepare atomic push operation", zap.Error(err))
			return fmt.Errorf("failed to prepare push operation: %w", err)
		}
	}

	// 5) packfile 데이터 읽기 및 unpack with progress tracking
	// 참고: ref updates 섹션 이후에 packfile이 raw binary로 전송됨
	// packfile이 있는지 확인 (peek으로 "PACK" 시그니처 확인)
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

		// NOTE: do NOT wait for EOF to detect packfile end.
		// In receive-pack, client typically keeps the connection open waiting for server response,
		// so EOF may never arrive. Instead, parse the streaming packfile; parser stops at pack end.
		s.logger.Info("Waiting for packfile data from client (client may be compressing objects)")
		packfileReadStart := time.Now()
		firstByteReader := &firstByteLogReader{reader: reader, logger: s.logger, start: packfileReadStart}

		// Real-time debug: log read progress every 2s until Parse() returns
		done := make(chan struct{})
		defer close(done)
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					bytes, lastProg, since, isPack, threshold := progressTracker.GetProgressDebug()
					fields := []zap.Field{
						zap.Int64("bytes_read", bytes),
						zap.Duration("since_last_read", since),
						zap.Duration("stall_threshold", threshold),
						zap.Bool("pack_phase", isPack),
						zap.Time("last_progress", lastProg),
					}
					if since > threshold/2 {
						s.logger.Warn("packfile_read_progress_approaching_stall", fields...)
					} else {
						s.logger.Info("packfile_read_progress", fields...)
					}
				}
			}
		}()

		// Fast path: persist received pack as pack+idx (not loose objects).
		// We still need to consume exactly one pack from the stream (no EOF); parse without storage
		// to determine pack end while teeing raw bytes into a temp file for persistence.
		tmpPack, err := os.CreateTemp("", "git-receive-pack-*.pack")
		if err != nil {
			return fmt.Errorf("failed to create temp pack file: %w", err)
		}
		tmpPath := tmpPack.Name()
		defer func() {
			_ = tmpPack.Close()
			_ = os.Remove(tmpPath)
		}()

		tee := io.TeeReader(firstByteReader, tmpPack)
		scanner := packfile.NewScanner(tee)
		sendUnpackError := func(e error) error {
			s.logger.Error("Failed to process packfile", zap.Error(e))
			if err := atomicPushHandler.HandlePushTimeout(); err != nil {
				s.logger.Error("Failed to handle push timeout", zap.Error(err))
			}
			_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", e.Error()))
			_, _ = writer.WriteString("0000")
			_ = conn.Flush()
			return e
		}

		// Consume exactly one PACK from the stream (no EOF required).
		// We can't use packfile.Parser here (requires seekable source when storage isn't provided).
		version, objCount, err := scanner.Header()
		if err != nil {
			return sendUnpackError(fmt.Errorf("read pack header: %w", err))
		}
		s.logger.Info("Packfile header received",
			zap.Uint32("version", version),
			zap.Uint32("objects", objCount))

		parseStart := time.Now()
		for i := uint32(0); i < objCount; i++ {
			if _, err := scanner.NextObjectHeader(); err != nil {
				return sendUnpackError(fmt.Errorf("read pack object header[%d/%d]: %w", i+1, objCount, err))
			}
			if _, _, err := scanner.NextObject(io.Discard); err != nil {
				return sendUnpackError(fmt.Errorf("read pack object[%d/%d]: %w", i+1, objCount, err))
			}
			// Periodic progress for decoding/consuming stage (avoid per-object spam)
			if (i+1)%1000 == 0 {
				bytes, _, _, _, _ := progressTracker.GetProgressDebug()
				s.logger.Info("Packfile consume progress",
					zap.Uint32("objects_consumed", i+1),
					zap.Uint32("objects_total", objCount),
					zap.Int64("bytes_read", bytes),
					zap.Duration("elapsed", time.Since(parseStart)))
			}
		}
		if _, err := scanner.Checksum(); err != nil {
			return sendUnpackError(fmt.Errorf("read/validate pack checksum: %w", err))
		}

		bytes, _, _, _, _ := progressTracker.GetProgressDebug()
		s.logger.Info("Packfile stream consumed",
			zap.Int64("bytes_read", bytes),
			zap.Uint32("objects", objCount),
			zap.Duration("duration", time.Since(parseStart)))

		// Persist: write raw pack into storer.PackfileWriter (pack+idx)
		if err := tmpPack.Close(); err != nil {
			return fmt.Errorf("failed to close temp pack file: %w", err)
		}

		f, err := os.Open(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to open temp pack for persist: %w", err)
		}
		defer func() { _ = f.Close() }()

		pw, ok := gitRepo.Storer.(storer.PackfileWriter)
		if !ok {
			persistErr := fmt.Errorf("storer does not support PackfileWriter (cannot persist pack+idx)")
			_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
			_, _ = writer.WriteString("0000")
			_ = conn.Flush()
			return persistErr
		}
		w, err := pw.PackfileWriter()
		if err != nil {
			persistErr := fmt.Errorf("failed to open packfile writer: %w", err)
			_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
			_, _ = writer.WriteString("0000")
			_ = conn.Flush()
			return persistErr
		}

		persistStart := time.Now()
		copied, err := io.Copy(w, f)
		if err != nil {
			_ = w.Close()
			persistErr := fmt.Errorf("failed to write packfile: %w", err)
			_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
			_, _ = writer.WriteString("0000")
			_ = conn.Flush()
			return persistErr
		}
		s.logger.Info("Packfile writer copy completed",
			zap.Int64("bytes_copied", copied),
			zap.Duration("elapsed", time.Since(persistStart)))

		closeStart := time.Now()
		if err := w.Close(); err != nil {
			if errors.Is(err, packfile.ErrReferenceDeltaNotFound) {
				// Thin pack: client sent REF_DELTA whose base is outside the pack.
				// Fallback: resolve deltas using repo base objects and store as loose (same as HTTP receive-pack).
				packfileData, readErr := os.ReadFile(tmpPath)
				if readErr != nil {
					persistErr := fmt.Errorf("thin pack fallback read temp file: %w", readErr)
					_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
					_, _ = writer.WriteString("0000")
					_ = conn.Flush()
					return persistErr
				}
				if thinErr := protocol.UnpackThinPackToLooseObjects(gitRepo, packfileData); thinErr != nil {
					persistErr := fmt.Errorf("thin pack not supported: %w", err)
					_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
					_, _ = writer.WriteString("0000")
					_ = conn.Flush()
					return persistErr
				}
				s.logger.Info("Thin pack resolved via loose-object fallback",
					zap.Int("packfile_bytes", len(packfileData)))
				// Fallback succeeded; continue to atomic ref update below.
			} else {
				persistErr := fmt.Errorf("failed to persist packfile: %w", err)
				_ = s.writePktLine(conn, fmt.Sprintf("unpack %s", persistErr.Error()))
				_, _ = writer.WriteString("0000")
				_ = conn.Flush()
				return persistErr
			}
		} else {
			s.logger.Info("Packfile writer closed",
				zap.Duration("elapsed", time.Since(closeStart)))
			s.logger.Info("Packfile persisted as pack+idx",
				zap.Int64("bytes_copied", copied),
				zap.Duration("duration", time.Since(persistStart)))
		}
		s.logger.Info("Successfully unpacked packfile",
			zap.Int64("bytes_read", bytes),
			zap.Duration("duration", time.Since(packfileReadStart)))

	} else {
		s.logger.Info("No packfile detected, skipping unpack")
	}

	// 7) Execute atomic push operation
	if len(refUpdates) > 0 {
		s.logger.Info("Executing atomic push operation", zap.String("operation_id", operationID))
		if err := atomicPushHandler.ExecutePushOperation(); err != nil {
			s.logger.Error("Failed to execute atomic push operation", zap.Error(err))
			// Send unpack error response for atomic failure
			if err := s.writePktLine(conn, fmt.Sprintf("unpack atomic push failed: %s", err.Error())); err != nil {
				return fmt.Errorf("failed to write atomic push error: %w", err)
			}
			if _, err := writer.WriteString("0000"); err != nil {
				return fmt.Errorf("failed to write flush: %w", err)
			}
			if err := conn.Flush(); err != nil {
				return fmt.Errorf("failed to flush: %w", err)
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
					rw, err := repositories.OpenRepositorySimple(repoPath)
					if err == nil && rw != nil {
						repo := rw.GetRepo()
						if repo != nil {
							_, cerr := repo.CommitObject(plumbing.NewHash(newHash))
							if cerr == nil {
								s.logger.Info("Repository readable after push",
									zap.String("operation_id", operationID),
									zap.String("ref", refName),
									zap.String("commit", newHash),
									zap.Int("attempt", attempt),
									zap.Duration("duration", time.Since(start)))
								return
							}
							err = cerr
						} else {
							err = fmt.Errorf("nil repo")
						}
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
					s.addCommitEventToStream(gitRepo, oldHash, newHash, refName, owner, repo, userID, events.EventTypePush)
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

// updateRef updates a Git reference
func (s *DuplexTCPServer) updateRef(repo *gogit.Repository, oldHash, newHash, refName, owner, repoName, userID string) error {
	// Parse hashes
	oldHashObj := plumbing.NewHash(oldHash)
	newHashObj := plumbing.NewHash(newHash)
	// Check if this is a delete operation (newHash is all zeros)
	if newHash == "0000000000000000000000000000000000000000" {
		// Delete the reference
		ref := plumbing.ReferenceName(refName)
		if err := repo.Storer.RemoveReference(ref); err != nil {
			return fmt.Errorf("failed to delete reference %s: %w", refName, err)
		}
		s.logger.Info("Deleted reference", zap.String("ref_name", refName))
		return nil
	}

	// Check if this is a create operation (oldHash is all zeros)
	if oldHash == "0000000000000000000000000000000000000000" {
		// Create new reference
		s.logger.Info("Creating new reference",
			zap.String("ref_name", refName),
			zap.String("new_hash", newHash),
			zap.String("new_hash_obj", newHashObj.String()))

		ref := plumbing.NewHashReference(plumbing.ReferenceName(refName), newHashObj)
		s.logger.Info("Created reference object",
			zap.String("ref_name", ref.Name().String()),
			zap.String("ref_target", ref.Target().String()))

		if err := repo.Storer.SetReference(ref); err != nil {
			s.logger.Error("Failed to set reference in storer",
				zap.String("ref_name", refName),
				zap.String("error", err.Error()),
				zap.String("error_type", fmt.Sprintf("%T", err)))
			return fmt.Errorf("failed to create reference %s: %w", refName, err)
		}
		s.logger.Info("Successfully created reference", zap.String("ref_name", refName), zap.String("hash", newHash))

		// Publish commit event if enabled (only for branches, not tags)
		if s.eventsClient != nil && strings.HasPrefix(refName, "refs/heads/") {
			s.addCommitEventToStream(repo, "", newHash, refName, owner, repoName, userID, events.EventTypePush)
		}
		return nil
	}

	// Update existing reference
	// First check if the old hash matches the current reference
	currentRef, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		// Ref가 존재하지 않는 경우, oldHash가 zero hash가 아니면 에러
		if err == plumbing.ErrReferenceNotFound {
			if oldHash != "0000000000000000000000000000000000000000" {
				return fmt.Errorf("reference %s does not exist (non-zero old hash provided)", refName)
			}
			// oldHash가 zero hash이고 ref가 없으면 생성으로 처리 (이미 위에서 처리됨)
			// 여기서는 실제로는 도달하지 않아야 함
			return fmt.Errorf("reference %s does not exist", refName)
		}
		return fmt.Errorf("failed to get current reference %s: %w", refName, err)
	}

	if currentRef.Hash() != oldHashObj {
		// Hash mismatch - 클라이언트가 최신 상태가 아님
		return fmt.Errorf("non-fast-forward: reference %s is at %s but expected %s",
			refName, currentRef.Hash().String(), oldHash)
	}

	// Update the reference
	ref := plumbing.NewHashReference(plumbing.ReferenceName(refName), newHashObj)
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to update reference %s: %w", refName, err)
	}

	s.logger.Info("Updated reference",
		zap.String("ref_name", refName),
		zap.String("old_hash", oldHash),
		zap.String("new_hash", newHash))

	// Publish commit event if enabled (only for branches, not tags, and only if hash changed)
	if s.eventsClient != nil && oldHash != newHash && strings.HasPrefix(refName, "refs/heads/") {
		s.addCommitEventToStream(repo, oldHash, newHash, refName, owner, repoName, userID, events.EventTypePush)
	}

	return nil
}

// handleLsRefs : ls-refs 명령 처리 (info/refs) with progress-aware connection
func (s *DuplexTCPServer) handleLsRefs(conn *connection.ProgressAwareConnection, owner, repo string, lsRefsReq *protocol.LsRefsReq) error {
	writer := conn.GetWriter()

	// ls-refs 응답 형식 (Git protocol v2):
	// 1. 직접 refs 목록만 전송 (capabilities는 보내지 않음)
	// 2. flush packet
	//
	// 각 ref 형식:
	// - 일반 ref: <hash> <refname>
	// - peeled tag (peel 옵션): <tag-hash> <refname>
	//                            peeled <commit-hash>
	// - symbolic ref (symrefs 옵션): @<target> <refname>

	// 실제 repository에서 refs 가져오기
	// Create path config
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	// Get repository path
	repoPath, err := repositories.GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repository path: %w", err)
	}

	// Open repository via cache so the subsequent fetch command gets a cache hit.
	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		pw := &protocol.PktWriter{W: writer, WithLF: false}
		if err := pw.Err("repository not found"); err != nil {
			return fmt.Errorf("failed to write error response: %w", err)
		}
		return err
	}
	gitRepo := repoWrapper.GetRepo()

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
		// If no ref-prefix filter, include HEAD
		headRequested = true
	}
	// Handle HEAD separately if requested
	if headRequested {
		headRef, err := gitRepo.Storer.Reference(plumbing.HEAD)

		// 첫 번째 시도: storer에서 HEAD 가져오기
		if err == nil {
			if headRef.Type() == plumbing.SymbolicReference && lsRefsReq.Symrefs {
				// symbolic HEAD 처리
				targetBranch := headRef.Target().String()
				targetRef, err := gitRepo.Reference(plumbing.ReferenceName(targetBranch), true)
				if err != nil {
					return fmt.Errorf("failed to resolve symbolic HEAD target: %w", err)
				}

				refLine := fmt.Sprintf("%s HEAD symref-target:%s", targetRef.Hash(), targetBranch)
				if err := s.writePktLine(conn, refLine); err != nil {
					return fmt.Errorf("failed to write HEAD symref: %w", err)
				}

			} else {
				// detached HEAD 또는 symref 요청이 없는 경우
				if resolved, err := gitRepo.Head(); err == nil {
					refLine := fmt.Sprintf("%s HEAD", resolved.Hash())
					if err := s.writePktLine(conn, refLine); err != nil {
						return fmt.Errorf("failed to write HEAD hash: %w", err)
					}
				}
			}
			// HEAD를 보낸 후에도 다른 refs를 계속 보내기 위해 return하지 않음
		} else {
			// 두 번째 시도: HEAD 파일 직접 읽기 (bare repo에서만 문제 생길 경우)
			headPath := filepath.Join(repoPath, "HEAD")
			data, err := os.ReadFile(headPath)
			if err == nil {
				headStr := strings.TrimSpace(string(data))

				// symbolic HEAD: "ref: refs/heads/main"
				if strings.HasPrefix(headStr, "ref: ") && lsRefsReq.Symrefs {
					targetBranch := strings.TrimPrefix(headStr, "ref: ")
					targetRef, err := gitRepo.Reference(plumbing.ReferenceName(targetBranch), true)
					if err != nil {
						return fmt.Errorf("failed to resolve symbolic HEAD file target: %w", err)
					}

					refLine := fmt.Sprintf("%s HEAD symref-target:%s", targetRef.Hash(), targetBranch)
					if err := s.writePktLine(conn, refLine); err != nil {
						return fmt.Errorf("failed to write HEAD symref: %w", err)
					}
				} else {
					// detached HEAD
					if resolved, err := gitRepo.Head(); err == nil {
						refLine := fmt.Sprintf("%s HEAD", resolved.Hash())
						if err := s.writePktLine(conn, refLine); err != nil {
							return fmt.Errorf("failed to write HEAD hash: %w", err)
						}
					}
				}
			}
			// HEAD가 없어도 다른 refs는 보내야 하므로 계속 진행
		}
	}

	// Get repository references
	refs, err := gitRepo.References()
	if err != nil {
		return fmt.Errorf("failed to get repository references: %w", err)
	}

	// List references in ls-refs format with filtering
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()

		// Skip HEAD as it's already handled separately above
		if refName == "HEAD" {
			return nil
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
				return nil // Skip this ref
			}
		}

		// Handle different reference types
		if ref.Type() == plumbing.HashReference {
			// Regular hash reference
			tagHash := ref.Hash().String()

			if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") {
				// For tags with peel option, send tag hash first, then peeled hash
				if tagObj, err := gitRepo.TagObject(ref.Hash()); err == nil && tagObj != nil {
					peeledHash := tagObj.Target.String()

					// 형식: <tag-hash> <refname>
					refLine := fmt.Sprintf("%s %s", tagHash, refName)
					if err := s.writePktLine(conn, refLine); err != nil {
						return err
					}

					// 형식: peeled <commit-hash>
					peeledLine := fmt.Sprintf("peeled %s", peeledHash)
					return s.writePktLine(conn, peeledLine)
				}
			}

			// 형식: <hash> <refname>
			refLine := fmt.Sprintf("%s %s", tagHash, refName)
			return s.writePktLine(conn, refLine)

		} else if ref.Type() == plumbing.SymbolicReference && lsRefsReq.Symrefs {
			// 심볼릭 참조 (예: HEAD -> refs/heads/main)
			target := ref.Target().String()
			refLine := fmt.Sprintf("@%s %s", target, refName)
			return s.writePktLine(conn, refLine)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to list references: %w", err)
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

// --- main: v2 side-band-64k로 PACK 전송 ---
func (s *DuplexTCPServer) writePackfileWithSideband(
	conn *connection.ProgressAwareConnection,
	st storer.EncodedObjectStorer,
	hashes []plumbing.Hash,
	fetchReq *protocol.FetchReq,
) error {
	writer := conn.GetWriter()
	// v2 packfile 스트림은 항상 side-band-64k.
	// OfsDelta 요청 시 보통 ref-delta를 끔.
	useRefDelta := !fetchReq.OfsDelta

	totalObjects := len(hashes)
	if totalObjects == 0 {
		return nil
	}

	// 채널 1(sideband=pack data) writer
	// 참고: Git protocol v2에서 "packfile" 섹션 헤더 이후에 packfile 데이터가
	// 즉시 시작되어야 합니다. Progress 메시지는 packfile 전송 중에 sideband channel 2로
	// 보낼 수 있지만, go-git의 encoder는 progress callback을 제공하지 않으므로
	// 프로토콜 오류를 피하기 위해 progress 메시지를 건너뜁니다.
	// [17-1] sideband writer 생성
	sbw := newSidebandWriter(writer, 0x01)

	// [17-2] packfile encoder 생성
	enc := packfile.NewEncoder(sbw, st, useRefDelta)

	// go-git v5: Encode(hashes []plumbing.Hash, packWindow uint)
	// 참고: go-git의 encoder는 progress callback을 제공하지 않으므로 인코딩 중에
	// compressing progress를 보낼 수 없습니다. packfile 인코딩 자체가 compressing 과정입니다.
	// packWindow=0: delta 압축 비활성화.
	// Docker volume 환경에서 window=N이면 object당 N회 random pack seek 발생.
	// 8149 objects × window=10 → 81490 seeks ≈ 30초 이상 → StallThreshold 초과.
	// delta 없이 보내면 pack 크기는 커지지만 인코딩 시간이 O(N)으로 줄어든다.
	packWindow := uint(0)
	// [17-3] packfile 인코딩 실행

	// Encode 실행 (panic recovery 포함)
	var encodeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				encodeErr = fmt.Errorf("panic in packfile encoding: %v", r)
			}
		}()
		_, encodeErr = enc.Encode(hashes, packWindow)
	}()

	if encodeErr != nil {
		return fmt.Errorf("encode pack (sideband): %w", encodeErr)
	}

	// 인코딩 완료 후 버퍼에 남은 pack 데이터를 전송.
	// 섹션 종료(0000)는 호출자(handleUploadPack)가 담당.
	// sbw.Flush()를 여기서 호출하면 0000이 먼저 나가고 이후 progress 메시지가
	// 섹션 외부로 전송돼 클라이언트 hang 발생.
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("socket flush after encoding: %w", err)
	}
	return nil
}

// getFetchObjectCount returns the number of commits to send (git rev-list --count --stdin)
// with the same wants/haves as pack-objects. Used for progress on the pack-objects path
// to avoid BuildFetchObjectList enumeration; count is commit count, not full object count.
func (s *DuplexTCPServer) getFetchObjectCount(repoPath string, fetchReq *protocol.FetchReq) (int, error) {
	args := []string{"rev-list", "--count", "--stdin"}
	if fetchReq.Deepen > 0 {
		args = append(args, "--depth="+strconv.Itoa(fetchReq.Deepen))
	}
	var stdinBuf bytes.Buffer
	for _, w := range fetchReq.Wants {
		stdinBuf.WriteString(w.String())
		stdinBuf.WriteByte('\n')
	}
	for _, h := range fetchReq.Haves {
		stdinBuf.WriteString("^")
		stdinBuf.WriteString(h.String())
		stdinBuf.WriteByte('\n')
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Stdin = &stdinBuf
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list --count output: %w", err)
	}
	return n, nil
}

// writePackfileViaGitPackObjects runs `git pack-objects --stdout --revs`, streams
// stdout into sideband (channel 0x01). want/have를 stdin에 rev-list 형식으로 넘김.
// --depth=N 지원(shallow). 실패 시 호출자가 writePackfileWithSideband로 폴백.
func (s *DuplexTCPServer) writePackfileViaGitPackObjects(
	ctx context.Context,
	conn *connection.ProgressAwareConnection,
	repoPath string,
	fetchReq *protocol.FetchReq,
) error {
	if len(fetchReq.Wants) == 0 {
		return fmt.Errorf("no wants")
	}
	args := []string{"pack-objects", "--stdout", "--revs"}
	if fetchReq.Deepen > 0 {
		args = append(args, "--depth="+strconv.Itoa(fetchReq.Deepen))
	}
	if fetchReq.ThinPack {
		args = append(args, "--thin")
	}
	if fetchReq.OfsDelta {
		args = append(args, "--delta-base-offset")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath

	var stdinBuf bytes.Buffer
	for _, w := range fetchReq.Wants {
		stdinBuf.WriteString(w.String())
		stdinBuf.WriteByte('\n')
	}
	for _, h := range fetchReq.Haves {
		stdinBuf.WriteString("^")
		stdinBuf.WriteString(h.String())
		stdinBuf.WriteByte('\n')
	}
	cmd.Stdin = &stdinBuf

	sbw := newSidebandWriter(conn.GetWriter(), 0x01)
	cmd.Stdout = sbw
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		stderr := stderrBuf.String()
		if stderr != "" {
			s.logger.Error("pack-objects failed", zap.String("stderr", stderr), zap.Error(err))
			return fmt.Errorf("%w (stderr: %s)", err, stderr)
		}
		return err
	}
	// 섹션 종료(0000)는 호출자가 progress 후 전송. sbw.Flush() 미호출로 writePackfileWithSideband와 동일.
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
func (s *DuplexTCPServer) writePktLine(conn *connection.ProgressAwareConnection, data string) error {
	// pkt-line 형식: [4자리 hex 길이][데이터]\n
	writer := conn.GetWriter()
	length := len(data) + 4
	line := fmt.Sprintf("%04x%s", length, data)
	_, err := writer.WriteString(line)
	return err
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

// addCommitEventToStream adds a commit event to Redis Stream
func (s *DuplexTCPServer) addCommitEventToStream(repo *gogit.Repository, oldHash, commitHash, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if s.eventsClient == nil {
		return
	}

	// Get commit object to verify it exists
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		s.logger.Debug("Skipping event publish; commit not found",
			zap.String("commit_hash", commitHash),
			zap.Error(err))
		return
	}

	// Get full filesystem path for the repository
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		s.logger.Debug("Failed to create path config for event",
			zap.Error(err))
		return
	}

	repoPath, err := repositories.GetRepoPath(pathConfig, owner, repoName)
	if err != nil {
		s.logger.Debug("Failed to get repo path for event",
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
		Pusher:        "git", // Default pusher identifier
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			s.logger.Warn("Failed to publish commit event",
				zap.String("repo_path", repoPath),
				zap.String("commit_hash", commitHash),
				zap.Error(err))
		}
	}()
}

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/connection"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/middleware"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/ruleguard"
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
	ruleClient            *ruleguard.Client
}

// Legacy DuplexConnection type alias for compatibility during migration
type DuplexConnection = connection.ProgressAwareConnection

const zeroHash = "0000000000000000000000000000000000000000"

// flushWriter wraps a bufio.Writer and flushes after every Write call.
// This ensures native git process output is immediately delivered to the TCP client,
// preventing protocol hangs where the client waits for buffered data.
type flushWriter struct {
	w *bufio.Writer
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if err != nil {
		return n, err
	}
	return n, fw.w.Flush()
}

// NewDuplexTCPServer creates a new duplex TCP server with progress-aware connection management.
// cc 가 nil 이 아니면 외부에서 주입된 ConcurrencyController 를 사용한다 (HTTP 서버와 슬롯 공유).
func NewDuplexTCPServer(config *config.Config, cc *connection.ConcurrencyController) (*DuplexTCPServer, error) {
	// Initialize logger
	logger, err := logging.NewLogger(config.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if config.Events.Enabled {
		eventsClient = events.NewLazyRedisStreamClient(config.Events, logger.Logger)
	}

	// Initialize metrics collector
	metrics := connection.NewMetricsCollector("git_executor")

	// Initialize timeout policy
	timeoutPolicy := connection.DefaultTimeoutPolicy()

	// Use injected controller or create a local one (backward compat)
	if cc == nil {
		concurrencyConfig := connection.DefaultConcurrencyConfig()
		cc = connection.NewConcurrencyController(concurrencyConfig, metrics, logger.Logger)
	}

	server := &DuplexTCPServer{
		config:                config,
		eventsClient:          eventsClient,
		connections:           make(map[net.Conn]*connection.ProgressAwareConnection),
		concurrencyController: cc,
		timeoutPolicy:         timeoutPolicy,
		metrics:               metrics,
		logger:                logger,
		ruleClient:            ruleguard.NewClient(config.Orchestrator.BaseURL, config.Orchestrator.Timeout),
	}

	logger.Info("Progress-aware Git executor server initialized",
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

			// Enable TCP keepalive for early dead-connection detection
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.SetKeepAlive(true)
				_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
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

	// NOTE: concurrencyController 는 main 에서 공유 인스턴스로 관리하므로 여기서 Stop 하지 않는다.
	// main.go 에서 프로세스 종료 시 한 번만 Stop 호출.

	// Close all active connections
	s.mu.Lock()
	for conn, progressConn := range s.connections {
		_ = progressConn.Close()
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
	defer func() { _ = progressConn.Close() }()

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
		remoteAddr := conn.RemoteAddr().String()
		classified := connection.ClassifyNetworkError(err, "git_protocol", remoteAddr, "operation")

		switch {
		case connection.IsClientDisconnect(classified):
			s.logger.Debug("Client disconnected",
				zap.String("remote_addr", remoteAddr),
				zap.Error(err))
			s.metrics.RecordConnectionError("client_disconnect", connection.OperationClone)
		case connection.IsTimeoutError(classified):
			s.logger.Warn("Connection timed out",
				zap.String("remote_addr", remoteAddr),
				zap.Error(err))
			s.metrics.RecordConnectionError("timeout", connection.OperationClone)
		default:
			s.logger.Error("Git protocol error",
				zap.String("remote_addr", remoteAddr),
				zap.Error(err))
			s.metrics.RecordConnectionError("protocol_error", connection.OperationClone)
		}
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

	// Parse optional key=value tokens (e.g., host=..., user-id=..., and the push
	// rule-validation context: repository-id / workspace-slug / decision-token).
	var pcRepositoryID, pcWorkspaceSlug, pcDecisionToken string
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
		switch k {
		case "user-id", "user_id", "userid", "uid":
			owner, repo, _, protoV2Prev := conn.GetConnectionInfo()
			conn.SetConnectionInfo(owner, repo, v, protoV2Prev)
		case "repository-id", "repository_id":
			pcRepositoryID = v
		case "workspace-slug", "workspace_slug":
			pcWorkspaceSlug = v
		case "decision-token", "decision_token":
			pcDecisionToken = v
		}
	}
	if pcRepositoryID != "" || pcWorkspaceSlug != "" || pcDecisionToken != "" {
		conn.SetPushContext(pcRepositoryID, pcWorkspaceSlug, pcDecisionToken)
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

	// 4) 서비스별 처리
	switch service {
	case "git-upload-pack":
		return s.handleUploadPack(conn, owner, repo)
	case "git-receive-pack":
		return s.handleReceivePack(conn, owner, repo)
	case "git-lfs-authenticate":
		return s.handleLFSAuthenticate(conn, owner, repo)
	default:
		return fmt.Errorf("unsupported Git service: %s", service)
	}
}

// handleUploadPack implements Git protocol v2 upload-pack (fetch/clone) in Go.
// Mirrors the SSH handler (processSSHUploadPack) by reusing the same pkt-line
// parsing, negotiation, and pack-objects logic instead of delegating to native
// git upload-pack process.
func (s *DuplexTCPServer) handleUploadPack(conn *connection.ProgressAwareConnection, owner, repo string) error {
	if err := conn.SetPhase(connection.PhaseOperation); err != nil {
		return fmt.Errorf("failed to set operation phase: %w", err)
	}

	// Concurrency control
	clientIP := connection.GetClientIP(conn.GetRemoteAddr())
	tenantID := fmt.Sprintf("%s/%s", owner, repo)
	ctx := conn.Context()
	if err := s.concurrencyController.RequestOperation(ctx, connection.OperationClone, tenantID, clientIP); err != nil {
		s.logger.Warn("Clone operation denied",
			zap.String("tenant", tenantID),
			zap.String("client_ip", clientIP),
			zap.Error(err))
		return fmt.Errorf("operation denied: %w", err)
	}
	operationStartTime := time.Now()
	var operationSuccess bool
	defer func() {
		s.concurrencyController.ReleaseOperation(connection.OperationClone, tenantID, clientIP)
		s.metrics.RecordOperationComplete(connection.OperationClone, time.Since(operationStartTime), operationSuccess, tenantID)
	}()

	// Resolve repository path
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}
	repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repository path: %w", err)
	}

	// pack-objects computation phase has no wire activity; use lenient stall threshold
	conn.GetProgressTracker().SetPackObjectsPhase(true)

	writer := conn.GetWriter()
	reader := conn.GetReader()

	s.logger.Info("Starting protocol v2 upload-pack",
		zap.String("repo", tenantID))

	// 1. Send Git protocol v2 advertisement
	if err := protocol.WriteV2Advert(writer, "git/collabops-git-executor", "ls-refs", "fetch=shallow filter"); err != nil {
		return fmt.Errorf("write v2 advert: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush v2 advert: %w", err)
	}

	pktWriter := performance.NewOptimizedPktLineWriter(writer)

	// 2. Command loop: ls-refs then fetch
	for {
		cmds, err := protocol.ReadPktLineSection(reader)
		if err != nil {
			return fmt.Errorf("read command section: %w", err)
		}
		if len(cmds) == 0 {
			break
		}

		if strings.HasPrefix(cmds[0], "command=ls-refs") {
			// Read ls-refs options (second section after delimiter)
			lsRefsOpts, err := protocol.ReadPktLineSection(reader)
			if err != nil {
				return fmt.Errorf("read ls-refs opts: %w", err)
			}
			allCmds := append(cmds, lsRefsOpts...)
			lsRefsReq, err := protocol.ParseLsRefsSection(allCmds)
			if err != nil {
				return fmt.Errorf("parse ls-refs: %w", err)
			}

			if err := protocol.HandleLsRefsNative(ctx, repoPath, lsRefsReq, pktWriter); err != nil {
				return fmt.Errorf("handle ls-refs: %w", err)
			}
			_ = pktWriter.Flush()
			_ = writer.Flush()
			continue // next command (fetch)

		} else if strings.HasPrefix(cmds[0], "command=fetch") {
			// Read fetch options (second section after delimiter)
			fetchOpts, err := protocol.ReadPktLineSection(reader)
			if err != nil {
				return fmt.Errorf("read fetch opts: %w", err)
			}
			allCmds := append(cmds, fetchOpts...)
			fetchReq, err := protocol.ParseFetchSection(allCmds)
			if err != nil {
				return fmt.Errorf("parse fetch: %w", err)
			}

			if err := protocol.ValidateFetchSupport(fetchReq); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.Flush()
				return fmt.Errorf("unsupported fetch option: %w", err)
			}

			// 3. Fetch negotiation: acknowledgments + ready
			// Filter haves to only those that exist on the server.
			// Clients may send OIDs that were force-pushed away or GC'd on the server;
			// passing unknown OIDs to pack-objects causes "fatal: bad object".
			existingHaves, err := gitcmd.FilterExistingObjects(ctx, repoPath, fetchReq.Haves)
			if err != nil {
				return fmt.Errorf("filter haves: %w", err)
			}
			var knownHaves []string
			if !fetchReq.Done {
				if err := pktWriter.WriteLine("acknowledgments"); err != nil {
					return fmt.Errorf("write acknowledgments: %w", err)
				}
				for _, have := range fetchReq.Haves {
					if existingHaves[have] {
						if err := pktWriter.WriteLine(fmt.Sprintf("ACK %s", have)); err != nil {
							return fmt.Errorf("write ACK: %w", err)
						}
						knownHaves = append(knownHaves, have)
					}
				}
				if len(knownHaves) == 0 {
					if err := pktWriter.WriteLine("NAK"); err != nil {
						return fmt.Errorf("write NAK: %w", err)
					}
				}
				if err := pktWriter.WriteLine("ready"); err != nil {
					return fmt.Errorf("write ready: %w", err)
				}
				// Delimiter (0001) — NOT flush (0000)
				if err := pktWriter.WriteDelim(); err != nil {
					return fmt.Errorf("write delim: %w", err)
				}
			} else {
				// Client sent "done" — still filter haves for pack-objects
				for _, have := range fetchReq.Haves {
					if existingHaves[have] {
						knownHaves = append(knownHaves, have)
					}
				}
			}

			// 4. Shallow-info section (if applicable)
			var shallowGrafts []string
			if fetchReq.Deepen > 0 {
				shallowGrafts, err = protocol.GetShallowBoundary(repoPath, fetchReq)
				if err != nil {
					return fmt.Errorf("shallow boundary: %w", err)
				}
			}
			if len(shallowGrafts) > 0 {
				if err := pktWriter.WriteLine("shallow-info"); err != nil {
					return fmt.Errorf("write shallow-info: %w", err)
				}
				for _, sh := range shallowGrafts {
					if err := pktWriter.WriteLine("shallow " + sh); err != nil {
						return fmt.Errorf("write shallow: %w", err)
					}
				}
				if err := pktWriter.WriteDelim(); err != nil {
					return fmt.Errorf("write shallow delim: %w", err)
				}
			}

			// 5. Packfile section header
			if err := pktWriter.WriteLine("packfile"); err != nil {
				return fmt.Errorf("write packfile header: %w", err)
			}

			// 6. Stream packfile via native git pack-objects with sideband encoding
			packShallow := append(fetchReq.Shallow, shallowGrafts...)
			sbWriter := protocol.NewSidebandWriter(writer, 0x01)
			packOpts := &gitcmd.PackObjectsOpts{
				Wants:         fetchReq.Wants,
				Haves:         knownHaves,
				Shallow:       packShallow,
				ThinPack:      fetchReq.ThinPack,
				OfsDelta:      fetchReq.OfsDelta,
				IncludeTag:    fetchReq.IncludeTag,
				DeepenSince:   fetchReq.DeepenSince,
				DeepenExclude: fetchReq.DeepenNot,
				Filter:        fetchReq.Filter,
				Transport:     "tcp",
			}
			if _, err := gitcmd.RunPackObjects(ctx, repoPath, packOpts, sbWriter); err != nil {
				return fmt.Errorf("pack-objects: %w", err)
			}
			if err := sbWriter.Flush(); err != nil {
				return fmt.Errorf("flush sideband: %w", err)
			}
			// Final pkt-line flush (0000) to signal end of response
			_ = pktWriter.Flush()
			_ = writer.Flush()

			operationSuccess = true
			s.logger.Info("Protocol v2 upload-pack completed",
				zap.String("repo", tenantID),
				zap.Duration("duration", time.Since(operationStartTime)))
			return nil

		} else {
			return fmt.Errorf("unknown v2 command: %s", cmds[0])
		}
	}

	operationSuccess = true
	return nil
}

// handleReceivePack delegates push to native git receive-pack.
// After the process completes, a ref snapshot diff detects which refs changed
// and publishes events + invalidates cache accordingly.
func (s *DuplexTCPServer) handleReceivePack(conn *connection.ProgressAwareConnection, owner, repo string) error {
	if err := conn.SetPhase(connection.PhaseOperation); err != nil {
		return fmt.Errorf("failed to set operation phase: %w", err)
	}

	// Concurrency control
	clientIP := connection.GetClientIP(conn.GetRemoteAddr())
	tenantID := fmt.Sprintf("%s/%s", owner, repo)
	ctx := conn.Context()
	if err := s.concurrencyController.RequestOperation(ctx, connection.OperationPush, tenantID, clientIP); err != nil {
		s.logger.Warn("Push operation denied",
			zap.String("tenant", tenantID),
			zap.String("client_ip", clientIP),
			zap.Error(err))
		return fmt.Errorf("operation denied: %w", err)
	}
	operationStartTime := time.Now()
	var operationSuccess bool
	defer func() {
		s.concurrencyController.ReleaseOperation(connection.OperationPush, tenantID, clientIP)
		s.metrics.RecordOperationComplete(connection.OperationPush, time.Since(operationStartTime), operationSuccess, tenantID)
	}()

	// Resolve repository path
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}
	repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repo path: %w", err)
	}
	if err := repositories.EnsureRepoExists(pathConfig, repoPath); err != nil {
		return fmt.Errorf("repository not found: %w", err)
	}

	// Repo write lock — prevents concurrent pack/ref writes and ensures snapshot diff safety
	repoLock := repositories.GetRepoRWLock(repoPath)
	s.logger.Info("repo_write_lock_acquire", zap.String("repo_path", repoPath))
	repoLock.Lock()
	s.logger.Info("repo_write_lock_acquired", zap.String("repo_path", repoPath))
	defer func() {
		s.logger.Info("repo_write_lock_release", zap.String("repo_path", repoPath))
		repoLock.Unlock()
	}()

	// Snapshot refs before push (for post-push event diff)
	refsBefore := s.snapshotRefs(ctx, repoPath)

	// Rule validation (ruleset enforcement). The push wire is: pkt-line command
	// section (ref updates) → flush → packfile. validatePushSection reads only the
	// command section, validates each branch ref against the repo's rules/rulesets
	// via the BFF, and re-splices the consumed command bytes ahead of the pack.
	//
	// LAZY (critical): in the interactive git protocol the client sends the command
	// section only AFTER receive-pack advertises its refs. We must NOT read the
	// client stream before git is spawned — that deadlocks (git hasn't advertised,
	// so the client hasn't sent commands, so a pre-spawn read blocks forever; this
	// was the SSH-push hang). Instead we wrap git's stdin and validate on git's
	// first read (by then git has advertised and the client has sent its commands).
	//
	// Dual-run: a transport/parse error fails open (the ssh gateway still validates
	// until cutover); a genuine rule violation surfaces as gitStdin.DenyErr().
	gitStdin := s.newPushValidatingReader(ctx, conn)

	// receive-pack indexes incoming packfiles (delta resolution, SHA verification)
	// which can stall the wire for large pushes; use the lenient threshold.
	conn.GetProgressTracker().SetPackObjectsPhase(true)

	// Spawn native git receive-pack — tied to connection context
	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "receive-pack", repoPath)
	// Meter the rule-validated stream (gitStdin re-splices the command section
	// consumed during validation ahead of the packfile — see validatePushSection)
	// so transfer metrics still count the full wire git actually reads.
	mr := gitcmd.NewMeteredReader(gitStdin)
	cmd.Stdin = mr
	cmd.Stdout = &flushWriter{conn.GetWriter()}
	cmd.Stderr = &stderrBuf

	s.logger.Info("Starting native git receive-pack",
		zap.String("repo", tenantID))

	// Network-independent server-processing metric for TCP push. CPUTime is
	// pure server compute (delta resolution + fsck) regardless of how slowly
	// the client uploaded the packfile.
	receiveStart := time.Now()
	runErr := cmd.Run()
	gitcmd.RecordTransfer(gitcmd.TransferStats{
		Operation: "receive-pack",
		Transport: "tcp",
		CPUTime:   gitcmd.CPUTime(cmd.ProcessState),
		WallTime:  time.Since(receiveStart),
		Bytes:     mr.Bytes(),
		Success:   runErr == nil,
	})
	// A rule violation surfaces as a stdin read error to git (non-zero exit); prefer
	// the structured deny over the generic git error so the gateway/client see it.
	if denied := gitStdin.DenyErr(); denied != nil {
		return denied // rule violation → reject
	}
	if runErr != nil {
		return fmt.Errorf("git receive-pack: %w (stderr: %s)", runErr, stderrBuf.String())
	}

	// Post-push processing
	operationSuccess = true
	s.logger.Info("Native git receive-pack completed",
		zap.String("repo", tenantID),
		zap.Duration("duration", time.Since(operationStartTime)))

	// Invalidate repository cache so overview/read paths see new data
	removed := repositories.GetSimpleCache().Invalidate(repoPath)
	s.logger.Info("Repository cache invalidated after push",
		zap.String("repo_path", repoPath),
		zap.Bool("was_cached", removed))

	// Post-push ref diff and event publishing use an independent context
	// because the connection context may already be cancelled at this point.
	postPushCtx, postPushCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer postPushCancel()

	// Detect ref changes via snapshot diff and publish events
	refsAfter := s.snapshotRefs(postPushCtx, repoPath)
	_, _, userID, _ := conn.GetConnectionInfo()
	s.publishRefChangeEvents(postPushCtx, repoPath, owner, repo, userID, refsBefore, refsAfter)

	// loose object / pack 누적 정리는 internal/maintenance 스케줄러가 임계값 기반
	// 주기 스캔으로 담당한다 — push 경로에서 git gc --auto 를 띄우지 않는다.
	// (여기 있던 비활성 runAutoGC 호출과 메서드는 Issue-6364 에서 제거)

	return nil
}

// pushValidatingReader lazily enforces ruleset rules on a receive-pack wire.
//
// It MUST be lazy: the interactive git protocol has receive-pack advertise its
// refs before the client sends the command section, so reading the client stream
// before git is spawned deadlocks. This reader is wired as git's stdin, so its
// first Read only happens after git has advertised and the client has replied —
// at which point the command section is available and ParseReceiveCommands won't
// block. On that first Read it parses+validates the command section, then serves
// the byte-identical wire (consumed command bytes re-spliced ahead of the pack).
//
// Dual-run: a transport/parse error fails open (the ssh gateway still validates);
// a genuine rule violation is captured in denyErr and returned from Read so git
// aborts, and handleReceivePack surfaces it via DenyErr().
type pushValidatingReader struct {
	ctx     context.Context
	s       *DuplexTCPServer
	conn    *connection.ProgressAwareConnection
	started bool
	stream  io.Reader // set on first Read: re-spliced wire (or raw reader on no-op)
	denyErr error     // non-nil ⇒ rule violation; returned from Read
}

func (s *DuplexTCPServer) newPushValidatingReader(ctx context.Context, conn *connection.ProgressAwareConnection) *pushValidatingReader {
	return &pushValidatingReader{ctx: ctx, s: s, conn: conn}
}

// DenyErr reports a rule violation detected during streaming (nil if allowed).
func (p *pushValidatingReader) DenyErr() error { return p.denyErr }

func (p *pushValidatingReader) Read(b []byte) (int, error) {
	if !p.started {
		p.started = true
		p.begin()
	}
	if p.denyErr != nil {
		return 0, p.denyErr
	}
	return p.stream.Read(b)
}

// begin runs once, on git's first stdin read — safe because git has already
// advertised by then, so the client's command section is in flight.
func (p *pushValidatingReader) begin() {
	reader := p.conn.GetReader()
	repositoryID, workspaceSlug, decisionToken := p.conn.GetPushContext()

	// Degrade open: no client or no forwarded rule context ⇒ don't touch the wire.
	if p.s.ruleClient == nil || repositoryID == "" || decisionToken == "" {
		p.stream = reader
		return
	}

	commands, rawConsumed, perr := protocol.ParseReceiveCommands(reader)
	// Always re-splice whatever we consumed so git gets the full, byte-identical wire.
	p.stream = io.MultiReader(bytes.NewReader(rawConsumed), reader)
	if perr != nil {
		p.s.logger.Warn("rule validation: failed to parse push command section (allowing — gateway enforces)",
			zap.Error(perr))
		return
	}

	_, _, userID, _ := p.conn.GetConnectionInfo()
	for _, cmd := range commands {
		branch, ok := strings.CutPrefix(cmd.RefName, "refs/heads/")
		if !ok {
			continue // only branch updates carry branch rules
		}
		err := p.s.ruleClient.ValidatePush(p.ctx, ruleguard.PushTarget{
			RepositoryID:  repositoryID,
			WorkspaceSlug: workspaceSlug,
			Branch:        branch,
			UserID:        userID,
			DecisionToken: decisionToken,
		})
		if err == nil {
			continue
		}
		var denied *ruleguard.Denied
		if errors.As(err, &denied) {
			p.denyErr = denied // rule violation → reject (returned from Read)
			return
		}
		// Transport/API error: fail-open during dual-run (gateway enforces).
		p.s.logger.Warn("rule validation error (allowing — gateway enforces)",
			zap.String("ref", cmd.RefName), zap.Error(err))
	}
}

// snapshotRefs returns a map of refName → objectHash for all refs in the repository.
func (s *DuplexTCPServer) snapshotRefs(ctx context.Context, repoPath string) map[string]string {
	refs, err := gitcmd.ListRefs(ctx, repoPath)
	if err != nil {
		s.logger.Debug("Failed to snapshot refs (may be empty repo)", zap.Error(err))
		return make(map[string]string)
	}
	snapshot := make(map[string]string, len(refs))
	for _, ref := range refs {
		snapshot[ref.RefName] = ref.ObjectName
	}
	return snapshot
}

// publishRefChangeEvents compares before/after ref snapshots and publishes
// commit events for any refs that were created, updated, or deleted.
func (s *DuplexTCPServer) publishRefChangeEvents(
	ctx context.Context,
	repoPath, owner, repo, userID string,
	before, after map[string]string,
) {
	// Detect created or updated refs
	for refName, newHash := range after {
		oldHash, existed := before[refName]
		if !existed {
			oldHash = zeroHash
		}
		if oldHash == newHash {
			continue
		}
		switch {
		case strings.HasPrefix(refName, "refs/heads/"):
			s.logger.Info("Ref changed",
				zap.String("ref", refName),
				zap.String("old", oldHash),
				zap.String("new", newHash))
			s.addCommitEventToStream(repoPath, oldHash, newHash, refName, owner, repo, userID, events.EventTypePush)
		case strings.HasPrefix(refName, "refs/tags/"):
			// Peel to commit SHA so annotated tag object SHAs are never exposed
			// downstream (Issue-2829 decision #4). Skip if it cannot be resolved.
			commitHash, err := gitcmd.PeelToCommit(ctx, repoPath, newHash)
			if err != nil {
				s.logger.Debug("Skipping tag event; cannot peel tag to commit",
					zap.String("ref", refName),
					zap.String("new", newHash),
					zap.Error(err))
				continue
			}
			oldRev := oldHash
			if oldHash != zeroHash {
				if peeled, peelErr := gitcmd.PeelToCommit(ctx, repoPath, oldHash); peelErr == nil {
					oldRev = peeled
				}
			}
			s.logger.Info("Tag changed",
				zap.String("ref", refName),
				zap.String("old", oldRev),
				zap.String("new", commitHash))
			s.addCommitEventToStream(repoPath, oldRev, commitHash, refName, owner, repo, userID, events.EventTypeTagPush)
		}
	}

	// Detect deleted refs
	for refName, oldHash := range before {
		if _, stillExists := after[refName]; stillExists {
			continue
		}
		switch {
		case strings.HasPrefix(refName, "refs/heads/"):
			s.logger.Info("Ref deleted",
				zap.String("ref", refName),
				zap.String("old", oldHash))
			s.publishRefDeleteEvent(repoPath, oldHash, refName, owner, repo, userID, events.EventTypeBranchDelete)
		case strings.HasPrefix(refName, "refs/tags/"):
			// Best-effort peel; the tag object usually still exists pre-GC.
			oldRev := oldHash
			if peeled, err := gitcmd.PeelToCommit(ctx, repoPath, oldHash); err == nil {
				oldRev = peeled
			}
			s.logger.Info("Tag deleted",
				zap.String("ref", refName),
				zap.String("old", oldRev))
			s.publishRefDeleteEvent(repoPath, oldRev, refName, owner, repo, userID, events.EventTypeTagDelete)
		}
	}
}

// handleLFSAuthenticate는 SSH 경유 LFS 인증 요청을 처리합니다.
// Git LFS 클라이언트가 SSH push/pull 시 `git-lfs-authenticate <repo> upload/download`를 호출하면
// LFS Batch API의 URL과 인증 정보를 JSON으로 반환합니다.
//
// LFS HTTP API 는 orchestrator + api-service 가 소유하므로 (Issue-3052) 응답
// 의 href 는 LFS_EXTERNAL_URL env 로 주입된 orchestrator base URL 을 가리킨다.
// 그 env 가 비어 있으면 클라이언트가 `git config lfs.url` 로 명시해야 한다.
func (s *DuplexTCPServer) handleLFSAuthenticate(conn *connection.ProgressAwareConnection, owner, repo string) error {
	if s.config.LFS.ExternalURL == "" {
		writer := conn.GetWriter()
		_, _ = writer.WriteString("error: LFS over SSH is not configured on this server (set LFS_EXTERNAL_URL or use `git config lfs.url`)\n")
		_ = writer.Flush()
		return fmt.Errorf("LFS_EXTERNAL_URL not configured")
	}

	// LFS 서버의 HTTP 엔드포인트 URL 구성. git-ssh 가 `repo` 로 resolved repository
	// PK 를 넘겨주므로, LFS 서버(orchestrator)의 canonical numeric 라우트
	// `/repositories/{key}/info/lfs` 를 가리킨다 (Platform Law 4 — int PK).
	// owner 는 로깅 용도로만 유지한다.
	lfsHref := fmt.Sprintf("%s/repositories/%s/info/lfs", s.config.LFS.ExternalURL, repo)

	// LFS authenticate 응답 (Git LFS 프로토콜 스펙)
	header := map[string]string{
		"Content-Type": "application/vnd.git-lfs+json",
	}
	expiresIn := 3600

	// SSH→HTTPS credential handoff: orchestrator 의 LFS HTTP API 는 인증을 요구하지만
	// (get_current_active_user) SSH git-lfs 클라이언트엔 HTTP 자격증명이 없다. 이미
	// 인증된 SSH user 의 UUID 로 short-lived decision token 을 발급해 header 에 실어
	// 후속 batch/verify/lock 요청을 인증한다. shared secret 미설정 또는 user-id 가 UUID 가
	// 아니면 token 을 생략하고 기존 동작(클라이언트 자격증명 helper / 401)으로 degrade.
	_, _, userID, _ := conn.GetConnectionInfo()
	if token, mintErr := s.mintLFSAuthToken(userID, owner, repo); mintErr != nil {
		s.logger.Warn("LFS auth token not minted; client must supply its own credentials",
			zap.String("owner", owner), zap.String("repo", repo), zap.Error(mintErr))
	} else {
		header["Authorization"] = "Bearer " + token
		expiresIn = int(lfsAuthTokenTTL.Seconds())
	}

	authResp := map[string]interface{}{
		"href":       lfsHref,
		"header":     header,
		"expires_in": expiresIn,
	}

	respBytes, err := json.Marshal(authResp)
	if err != nil {
		return fmt.Errorf("failed to marshal LFS auth response: %w", err)
	}

	writer := conn.GetWriter()
	_, err = writer.Write(respBytes)
	if err != nil {
		return fmt.Errorf("failed to write LFS auth response: %w", err)
	}
	_, _ = writer.WriteString("\n")
	_ = writer.Flush()

	s.logger.Info("LFS authenticate completed",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("href", lfsHref))

	return nil
}

// lfsAuthTokenTTL bounds how long the minted LFS auth token is valid. Short by
// design — it only needs to cover a single git-lfs transfer negotiation.
const lfsAuthTokenTTL = 5 * time.Minute

// mintLFSAuthToken mints a short-lived decision token authenticating the SSH user
// to the orchestrator's LFS HTTP API. Returns an error (token omitted, logged) when
// signing is disabled (no shared secret) or userID is not a UUID — the handler then
// degrades to the prior behaviour rather than failing the authenticate.
func (s *DuplexTCPServer) mintLFSAuthToken(userID, owner, repo string) (string, error) {
	signer := middleware.NewDecisionSigner(s.config.Security.DecisionJWT)
	object := fmt.Sprintf("repository:%s/%s", owner, repo)
	return signer.Mint(userID, object, "git.lfs", lfsAuthTokenTTL, time.Now())
}

// readPktLine reads a single pkt-line frame from the reader.
// Used by handleGitProtocol for the initial service line handshake.
func readPktLine(r *bufio.Reader) ([]byte, bool, error) {
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
				_ = progressConn.Close()
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
		PushedAt:      time.Now(),
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

// publishRefDeleteEvent publishes a ref deletion event (branch_delete or
// tag_delete) to Redis Stream. Unlike addCommitEventToStream, this does not call
// GetCommitInfo because the ref has already been deleted and the commit hash is
// the old (pre-delete) value.
func (s *DuplexTCPServer) publishRefDeleteEvent(repoPath, oldHash, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if s.eventsClient == nil {
		return
	}

	relativePath := events.GetRelativeRepoPath(repoPath, s.config.Git.RepoRoot)

	event := &events.CommitEvent{
		RepoPath:   relativePath,
		EventType:  eventType,
		CommitHash: oldHash,
		OldRev:     oldHash,
		RefName:    refName,
		Pusher:     "git",
		UserID:     userID,
		Timestamp:  time.Now(),
		PushedAt:   time.Now(),
	}

	go func() {
		evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.eventsClient.AddCommitEventToStream(evCtx, event); err != nil {
			s.logger.Warn("Failed to publish ref delete event",
				zap.String("repo_path", repoPath),
				zap.String("ref", refName),
				zap.String("event_type", string(eventType)),
				zap.Error(err))
		}
	}()
}

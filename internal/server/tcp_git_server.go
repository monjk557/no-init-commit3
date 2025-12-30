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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
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
	config       *config.Config
	eventsClient events.RedisStreamClient
	listener     net.Listener
	connections  map[net.Conn]*DuplexConnection
	mu           sync.RWMutex
	logger       *logging.Logger
}

// DuplexConnection represents a bidirectional connection
type DuplexConnection struct {
	conn     net.Conn
	reader   *bufio.Reader
	writer   *bufio.Writer
	owner    string
	repo     string
	lastUsed time.Time
	protoV2  bool
}

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

// NewDuplexTCPServer creates a new duplex TCP server
func NewDuplexTCPServer(config *config.Config) (*DuplexTCPServer, error) {
	// Create permission client

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

	return &DuplexTCPServer{
		config:       config,
		eventsClient: eventsClient,
		connections:  make(map[net.Conn]*DuplexConnection),
		logger:       logger,
	}, nil
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

// Stop stops the TCP server
func (s *DuplexTCPServer) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// handleConnection : TCP 연결 처리
func (s *DuplexTCPServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	// [TCP] 연결 수락

	// Set read/write timeouts for the connection
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		tcpConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}

	// Create duplex connection
	duplexConn := &DuplexConnection{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		lastUsed: time.Now(),
	}

	// Register connection
	s.mu.Lock()
	s.connections[conn] = duplexConn
	s.mu.Unlock()

	// Cleanup on exit
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		// [TCP] 연결 종료
	}()

	// Handle Git protocol negotiation
	if err := s.handleGitProtocol(duplexConn); err != nil {
		// [TCP] 프로토콜 에러
		return
	}
}

// handleGitProtocol : Git 프로토콜 핸드셰이크 처리
func (s *DuplexTCPServer) handleGitProtocol(conn *DuplexConnection) error {
	// [1] 핸드셰이크 시작

	// 1) 첫 pkt-line (service line) 읽기
	line, isFlush, err := readPktLine(conn.reader)
	if err != nil {
		return fmt.Errorf("failed to read first pkt-line: %w", err)
	}
	if isFlush {
		return fmt.Errorf("unexpected flush before service line")
	}
	// [2] 서비스 라인 수신

	// 2) (선택) 추가 pkt-line 수집: 예) "version=2"
	var protoV2 bool
	for {
		l, flush, err := readPktLine(conn.reader)
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

	// 3) service line 파싱: "<service> /owner/repo.git\0host=..\0"
	// NUL(\x00) 기준으로 쪼갬
	parts0 := bytes.Split(line, []byte{0x00})
	if len(parts0) == 0 || len(parts0[0]) == 0 {
		return fmt.Errorf("invalid service line (empty)")
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
	conn.owner = owner
	conn.repo = repo

	// [3] 파싱 완료: service, owner, repo, v2

	// (원하면) proto_v2를 이후 단계에서 환경변수로 넘길 수 있도록 conn에 표시
	conn.protoV2 = protoV2 // 필요 시 DuplexConnection에 bool 필드 추가

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

// handleUploadPack : git-upload-pack (fetch, clone) 처리
func (s *DuplexTCPServer) handleUploadPack(conn *DuplexConnection, owner, repo string) error {
	// [4] upload-pack 처리 시작

	// 1) v2 광고 (ls-refs, fetch 지원)
	if err := protocol.WriteV2Advert(conn.writer, "git-executor/0.1.0", "ls-refs", "fetch"); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("flush advert: %w", err)
	}
	// [5] v2 광고 전송 완료

	// 2) 동기 상태머신 (한 섹션씩 처리)
	for {
		cmds, err := s.readSection(conn.reader) // delim/flush까지 읽어들임
		if err != nil {
			return err
		}
		if len(cmds) == 0 {
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
			lsRefsOpts, err := s.readSection(conn.reader)
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
			// [8] fetch 명령 수신
			// fetch 옵션 섹션 읽기 (command 섹션 다음 섹션)
			fetchCmds, err := s.readSection(conn.reader)
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

			// 저장소 열기 (캐시 사용)
			pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
			if err != nil {
				return fmt.Errorf("failed to create path config: %w", err)
			}
			repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
			if err != nil {
				return fmt.Errorf("failed to get repository path: %w", err)
			}
			// [9] 저장소 열기
			// 캐시를 사용하여 저장소 열기
			repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
			if err != nil {
				// 프로토콜 호환 에러 응답
				pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
				if err := pw.Err("repository not found"); err != nil {
					return fmt.Errorf("failed to write error response: %w", err)
				}
				_ = conn.writer.Flush()
				return nil
			}
			defer repoWrapper.Close()

			// 내부의 *gogit.Repository 가져오기 (캐시에서)
			underlyingRepo := repoWrapper.GetRepo()

			// 전송 대상 오브젝트 계산
			// [10] 오브젝트 계산 시작
			objectHashes, err := protocol.BuildFetchObjectList(underlyingRepo.Storer, underlyingRepo, fetchReq)
			if err != nil {
				return fmt.Errorf("failed to build fetch object list: %w", err)
			}
			// [11] 오브젝트 계산 완료

			// === 협상 섹션 처리 ===
			// 스펙: client가 "done"을 보냈다면(clone 경로) acknowledgments 섹션은 "생략"
			//      done이 없다면(pull 경로) acknowledgments 섹션을 "반드시" 보낸 뒤 delim(0001)
			if !fetchReq.Done {
				// [12] acknowledgments 섹션 전송
				// acknowledgments 헤더
				if err := s.writePktLine(conn, "acknowledgments"); err != nil {
					return fmt.Errorf("failed to write acknowledgments header: %w", err)
				}
				if len(fetchReq.Haves) == 0 {
					// 공통 오브젝트 없음 → NAK
					if err := s.writePktLine(conn, "NAK"); err != nil {
						return fmt.Errorf("failed to write NAK: %w", err)
					}
				} else {
					// 최소 구현: ready 하나만 보내도 됨(ACK 라인 생략 가능)
					if err := s.writePktLine(conn, "ready"); err != nil {
						return fmt.Errorf("failed to write ready: %w", err)
					}
				}
				// 섹션 구분자(delim)
				if _, err := conn.writer.WriteString("0001"); err != nil {
					return fmt.Errorf("failed to write delim-pkt: %w", err)
				}
			} else {
				// [12] acknowledgments 섹션 생략 (done)
			}

			// === packfile 섹션 ===
			// objectHashes가 있을 때만 packfile 섹션을 열어 전송
			if len(objectHashes) > 0 {
				// [13] packfile 섹션 시작
				// "packfile" 섹션 헤더
				// 참고: Git protocol v2에서 "packfile" 섹션 헤더 이후에 sideband 데이터를 보낼 수 있습니다
				// Progress 메시지는 "packfile" 섹션 헤더 이후에 보내야 하며, 이전에 보내면 안 됩니다
				if err := protocol.WritePackfileSectionHeader(conn.writer); err != nil {
					return fmt.Errorf("failed to write packfile section header: %w", err)
				}
				// "packfile" 헤더가 packfile 데이터 전에 전송되도록 flush
				if err := conn.writer.Flush(); err != nil {
					return fmt.Errorf("failed to flush packfile section header: %w", err)
				}
				// [14] packfile 헤더 전송 완료

				// "packfile" 섹션 헤더 이후, packfile 데이터 전에 progress 메시지 전송
				// 이들은 sideband channel 2 (progress)로 전송됩니다
				totalObjects := len(objectHashes)
				// [15] progress 메시지 전송 시작
				if err := writeProgressMessage(conn.writer, fmt.Sprintf("Enumerating objects: %d, done.\n", totalObjects)); err != nil {
					return fmt.Errorf("failed to write enumerating message: %w", err)
				}
				if err := writeProgressMessage(conn.writer, fmt.Sprintf("Counting objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
					return fmt.Errorf("failed to write counting completion: %w", err)
				}
				// [16] progress 메시지 전송 완료

				// Flush progress messages before starting packfile transmission
				// This ensures progress messages are sent before packfile data
				if err := conn.writer.Flush(); err != nil {
					return fmt.Errorf("failed to flush progress messages: %w", err)
				}

				// side-band-64k로 pack 전송 (채널 1=pack, 2=progress, 3=error)
				// Progress 메시지는 writePackfileWithSideband 내부에서 전송됩니다
				// [17] packfile 인코딩 시작
				if err := s.writePackfileWithSideband(conn, underlyingRepo.Storer, objectHashes, fetchReq); err != nil {
					return fmt.Errorf("failed to write packfile: %w", err)
				}
				// [18] packfile 인코딩 완료

				// packfile 인코딩 후 compressing 완료 및 요약 전송
				// [19] compressing 완료 메시지 전송
				if err := writeProgressMessage(conn.writer, fmt.Sprintf("Compressing objects: 100%% (%d/%d), done.\n", totalObjects, totalObjects)); err != nil {
					return fmt.Errorf("failed to write compressing completion: %w", err)
				}

				// delta 개수 계산 (간단한 추정)
				deltaCount := totalObjects / 8 // 대략적인 추정: ~12%가 delta입니다

				// 최종 요약 전송
				summary := fmt.Sprintf("Total %d (delta %d), reused %d (delta %d), pack-reused 0 (from 0)\n",
					totalObjects, deltaCount, totalObjects, deltaCount)
				if err := writeProgressMessage(conn.writer, summary); err != nil {
					return fmt.Errorf("failed to write summary: %w", err)
				}
				// [20] 요약 메시지 전송 완료
			}

			// 최종 flush
			// [21] 최종 flush
			if _, err := conn.writer.WriteString("0000"); err != nil {
				return fmt.Errorf("failed to write final flush: %w", err)
			}
			if err := conn.writer.Flush(); err != nil {
				return fmt.Errorf("failed to flush writer: %w", err)
			}
			// [22] 모든 처리 완료
			return nil
		}
	}
}

// handleReceivePack : git-receive-pack (push) 처리
func (s *DuplexTCPServer) handleReceivePack(conn *DuplexConnection, owner, repo string) error {

	// 1) Repository 열기
	pathConfig, err := repositories.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	repoPath, err := repositories.GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get repo path: %w", err)
	}

	s.logger.Info("Opening repository", zap.String("repo_path", repoPath))
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		s.logger.Error("Failed to open repository",
			zap.String("repo_path", repoPath),
			zap.Error(err))
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// 2) V1 광고 전송
	if err := protocol.WriteV1Advert(conn.writer, *gitRepo); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.writer.Flush(); err != nil {
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
		line, isFlush, err := readPktLine(conn.reader)
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
				// 첫 번째 읽기에서 바로 flush가 오면 클라이언트가 아직 데이터를 보내지 않았을 수 있음
				// 짧은 대기 후 다시 시도
				s.logger.Info("Received flush immediately after advertisement, waiting briefly for ref updates")
				time.Sleep(100 * time.Millisecond)
				// 연결의 읽기 타임아웃 갱신
				if tcpConn, ok := conn.conn.(*net.TCPConn); ok {
					tcpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
				}
				firstRead = false
				continue
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

	// 4) 실제 ref 업데이트 수행
	workTree, err := gitRepo.Worktree()
	if err != nil {
		s.logger.Info("Repository is bare (no worktree)", zap.String("repo_path", repoPath))
	} else {
		s.logger.Info("Repository has worktree",
			zap.String("repo_path", repoPath),
			zap.String("worktree_path", workTree.Filesystem.Root()))
	}

	// 5) packfile 데이터 읽기 및 unpack
	// 참고: ref updates 섹션 이후에 packfile이 raw binary로 전송됨
	// packfile이 있는지 확인 (peek으로 "PACK" 시그니처 확인)
	hasPackfile := false
	if peekData, err := conn.reader.Peek(4); err == nil && len(peekData) == 4 {
		if string(peekData) == "PACK" {
			hasPackfile = true
		}
	}

	if hasPackfile {
		s.logger.Info("Starting packfile read")

		// Idle 기반(silding) read deadline:
		// - 총 처리 시간이 아니라 "마지막으로 데이터를 받은 시점"부터의 유휴 시간 기준으로 타임아웃
		// - 대용량(1GB/2GB+)이라도 전송이 계속되면 타임아웃되지 않음
		idleTimeout := 30 * time.Second
		var packReader io.Reader = conn.reader // 중요: pkt-line 리더 말고, 원본 io.Reader
		if tcpConn, ok := conn.conn.(*net.TCPConn); ok {
			packReader = &deadlineRefreshingReader{
				r:    conn.reader,
				conn: tcpConn,
				idle: idleTimeout,
			}
			s.logger.Debug("Using idle-based read deadline for packfile parsing", zap.Duration("idle_timeout", idleTimeout))
		}

		scanner := packfile.NewScanner(packReader)
		parser, err := packfile.NewParserWithStorage(scanner, gitRepo.Storer)
		if err != nil {
			return fmt.Errorf("failed to create packfile parser: %w", err)
		}

		// 6) packfile 데이터 디코드
		if _, err := parser.Parse(); err != nil {
			s.logger.Error("Failed to parse packfile", zap.Error(err))
			// Send unpack error response
			if err := s.writePktLine(conn, fmt.Sprintf("unpack %s", err.Error())); err != nil {
				return fmt.Errorf("failed to write unpack error: %w", err)
			}
			if _, err := conn.writer.WriteString("0000"); err != nil {
				return fmt.Errorf("failed to write flush: %w", err)
			}
			if err := conn.writer.Flush(); err != nil {
				return fmt.Errorf("failed to flush: %w", err)
			}
			return err
		}

		s.logger.Info("Successfully unpacked packfile")
	} else {
		s.logger.Info("No packfile detected, skipping unpack")
	}

	// 7) unpack 성공 응답 전송
	if err := s.writePktLine(conn, "unpack ok"); err != nil {
		return fmt.Errorf("failed to write unpack ok: %w", err)
	}

	// 8) 각 ref 업데이트 처리 및 응답 전송
	if len(refUpdates) == 0 {
		s.logger.Warn("No ref updates received from client - this may indicate a protocol issue or empty push")
		// ref updates가 없으면 "unpack ok"만 보내고 종료
		// (클라이언트가 push할 내용이 없으면 ref updates가 없을 수 있음)
	} else {
		for _, refUpdate := range refUpdates {
			// Parse ref update line: "old-hash new-hash ref-name\0capabilities" or "old-hash new-hash ref-name capabilities"
			// Handle null-separated format first
			var refName string
			var oldHash, newHash string

			if strings.Contains(refUpdate, "\x00") {
				// Null-separated format: "old-hash new-hash ref-name\0capabilities"
				parts := strings.SplitN(refUpdate, "\x00", 2)
				refAndHashes := parts[0]
				hashParts := strings.Fields(refAndHashes)
				if len(hashParts) >= 3 {
					oldHash = hashParts[0]
					newHash = hashParts[1]
					refName = hashParts[2]
				}
			} else {
				// Space-separated format: "old-hash new-hash ref-name [capabilities]"
				parts := strings.Fields(refUpdate)
				if len(parts) >= 3 {
					oldHash = parts[0]
					newHash = parts[1]
					refName = parts[2]
				}
			}

			if oldHash == "" || newHash == "" || refName == "" {
				s.logger.Warn("Invalid ref update format", zap.String("update", refUpdate))
				continue
			}

			s.logger.Info("Processing ref update",
				zap.String("old_hash", oldHash),
				zap.String("new_hash", newHash),
				zap.String("ref_name", refName))

			// ref 업데이트 수행
			if err := s.updateRef(gitRepo, oldHash, newHash, refName, owner, repo); err != nil {
				s.logger.Error("Failed to update ref",
					zap.String("ref_name", refName),
					zap.Error(err))

				// Send error response for this ref
				statusLine := fmt.Sprintf("ng %s %s", refName, err.Error())
				if err := s.writePktLine(conn, statusLine); err != nil {
					return fmt.Errorf("failed to write ref error status: %w", err)
				}
				continue
			}

			// Send success response for this ref
			statusLine := fmt.Sprintf("ok %s", refName)
			if err := s.writePktLine(conn, statusLine); err != nil {
				return fmt.Errorf("failed to write ref success status: %w", err)
			}
		}
	}

	// 9) 최종 flush 패킷 전송
	if _, err := conn.writer.WriteString("0000"); err != nil {
		return fmt.Errorf("failed to write final flush: %w", err)
	}
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush writer: %w", err)
	}

	s.logger.Info("Push request processed successfully")
	return nil
}

// updateRef updates a Git reference
func (s *DuplexTCPServer) updateRef(repo *gogit.Repository, oldHash, newHash, refName, owner, repoName string) error {
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
			s.addCommitEventToStream(repo, "", newHash, refName, owner, repoName, events.EventTypePush)
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
		s.addCommitEventToStream(repo, oldHash, newHash, refName, owner, repoName, events.EventTypePush)
	}

	return nil
}

// handleLsRefs : ls-refs 명령 처리 (info/refs)
func (s *DuplexTCPServer) handleLsRefs(conn *DuplexConnection, owner, repo string, lsRefsReq *protocol.LsRefsReq) error {

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

	// Open repository
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		// Send error response according to Git protocol
		pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
		if err := pw.Err("repository not found"); err != nil {
			return fmt.Errorf("failed to write error response: %w", err)
		}
		return err
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
	pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
	if err := pw.Flush(); err != nil {
		return fmt.Errorf("failed to write flush packet: %w", err)
	}

	return conn.writer.Flush()
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
	conn *DuplexConnection,
	st storer.EncodedObjectStorer,
	hashes []plumbing.Hash,
	fetchReq *protocol.FetchReq,
) error {
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
	sbw := newSidebandWriter(conn.writer, 0x01)

	// [17-2] packfile encoder 생성
	enc := packfile.NewEncoder(sbw, st, useRefDelta)

	// go-git v5: Encode(hashes []plumbing.Hash, packWindow uint)
	// 참고: go-git의 encoder는 progress callback을 제공하지 않으므로 인코딩 중에
	// compressing progress를 보낼 수 없습니다. packfile 인코딩 자체가 compressing 과정입니다.
	packWindow := uint(0) // 지금은 delta 검색 비활성화
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
	// [17-4] packfile 인코딩 완료

	// packfile encoder가 완료된 후, sideband writer의 버퍼에 남아있는 데이터가 있을 수 있으므로
	// 먼저 underlying writer를 flush하여 모든 packfile 데이터(트레일러 포함)가 전송되도록 보장
	// [17-5] writer flush (packfile 데이터 전송 보장)
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("socket flush after encoding: %w", err)
	}

	// sideband 스트림 종료 (0000 flush 패킷 전송)
	// [17-6] sideband flush (0000)
	if err := sbw.Flush(); err != nil {
		return fmt.Errorf("sideband flush: %w", err)
	}

	// 최종 flush
	// [17-7] 최종 writer flush
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("socket flush: %w", err)
	}
	return nil
}

// readV2Section : v2 섹션 읽기 (flush 패킷 전까지)
func (s *DuplexTCPServer) readSection(reader *bufio.Reader) ([]string, error) {
	var commands []string

	for {
		// 1) 4바이트 길이 헤더 (hex)
		hdr := make([]byte, 4)

		if _, err := io.ReadFull(reader, hdr); err != nil {
			return nil, fmt.Errorf("read len hdr: %w", err)
		}

		// 2) flush 패킷인지 확인
		if string(hdr) == "0000" {
			// 섹션 끝 → 루프 탈출
			break
		}

		// 3) 길이 파싱
		n64, err := strconv.ParseUint(string(hdr), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("bad pkt-len %q: %w", string(hdr), err)
		}
		n := int(n64)

		// 0001은 delimiter 패킷 (capabilities와 데이터 구분)
		// delimiter를 만나면 현재 섹션을 종료하고 반환
		if n == 1 {
			break
		}

		if n < 4 {
			return nil, fmt.Errorf("invalid pkt-len: %d", n)
		}

		// 4) payload 정확히 n-4바이트 읽기
		payload := make([]byte, n-4)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, fmt.Errorf("read payload: %w", err)
		}

		// 5) 명령 추가 (개행문자 포함될 수 있으므로 trim)
		cmdStr := strings.TrimSpace(string(payload))
		if cmdStr != "" {
			commands = append(commands, cmdStr)
		}
	}
	return commands, nil
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

// writePktLine : pkt-line 전송
func (s *DuplexTCPServer) writePktLine(conn *DuplexConnection, data string) error {
	// pkt-line 형식: [4자리 hex 길이][데이터]\n
	length := len(data) + 4
	line := fmt.Sprintf("%04x%s", length, data)
	_, err := conn.writer.WriteString(line)
	return err
}

// cleanupConnections : 유휴 연결 정리
func (s *DuplexTCPServer) cleanupConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for conn, duplexConn := range s.connections {
			if now.Sub(duplexConn.lastUsed) > 5*time.Minute {
				conn.Close()
				delete(s.connections, conn)
			}
		}
		s.mu.Unlock()
	}
}

// addCommitEventToStream adds a commit event to Redis Stream
func (s *DuplexTCPServer) addCommitEventToStream(repo *gogit.Repository, oldHash, commitHash, refName, owner, repoName string, eventType events.CommitEventType) {
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
		RepoPath:   relativePath,
		EventType:  eventType,
		CommitHash: commitHash,
		OldRev:     oldHash,
		RefName:    refName,
		Pusher:     "git", // Default pusher identifier
		Timestamp:  commit.Committer.When,
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

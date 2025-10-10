package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repository"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"go.uber.org/zap"
)

// DuplexTCPServer handles bidirectional TCP communication for Git operations
type DuplexTCPServer struct {
	config           *config.Config
	permissionClient permissions.PermissionClient
	listener         net.Listener
	connections      map[net.Conn]*DuplexConnection
	mu               sync.RWMutex
	logger           *logging.Logger
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

type sidebandWriter struct {
	w         io.Writer
	channel   byte // 1=pack, 2=progress, 3=fatal
	chunkSize int  // payload(채널 포함) 최대 길이
}

// NewDuplexTCPServer creates a new duplex TCP server
func NewDuplexTCPServer(config *config.Config) (*DuplexTCPServer, error) {
	// Create permission client
	permClient, err := permissions.NewPermissionClient(permissions.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create permission client: %w", err)
	}

	// Initialize logger
	logger, err := logging.NewLogger(config.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	return &DuplexTCPServer{
		config:           config,
		permissionClient: permClient,
		connections:      make(map[net.Conn]*DuplexConnection),
		logger:           logger,
	}, nil
}

// Start starts the TCP server on configured port
func (s *DuplexTCPServer) Start() error {
	port := s.config.Server.TCPGitPort
	// TCPGitPort는 환경변수에서 필수로 로드되므로 fallback 불필요

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
		s.logger.Info("TCP connection closed",
			zap.String("remote_addr", conn.RemoteAddr().String()))
	}()
	s.logger.Info("handleConnection", zap.String("remote_addr", conn.RemoteAddr().String()))
	// Handle Git protocol negotiation
	if err := s.handleGitProtocol(duplexConn); err != nil {
		s.logger.Error("Git protocol error",
			zap.String("remote_addr", conn.RemoteAddr().String()),
			zap.Error(err))
		return
	}
}

// handleGitProtocol : Git 프로토콜 핸드셰이크 처리
func (s *DuplexTCPServer) handleGitProtocol(conn *DuplexConnection) error {
	s.logger.Info("handleGitProtocol", zap.String("remote_addr", conn.conn.RemoteAddr().String()))

	// 1) 첫 pkt-line (service line) 읽기
	line, isFlush, err := readPktLine(conn.reader)
	if err != nil {
		return fmt.Errorf("failed to read first pkt-line: %w", err)
	}
	if isFlush {
		return fmt.Errorf("unexpected flush before service line")
	}
	s.logger.Info("Received service pkt-line", zap.ByteString("payload", line))

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

	s.logger.Info("Parsed handshake",
		zap.String("service", service),
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.Bool("proto_v2", protoV2),
	)

	// (원하면) proto_v2를 이후 단계에서 환경변수로 넘길 수 있도록 conn에 표시
	conn.protoV2 = protoV2 // 필요 시 DuplexConnection에 bool 필드 추가

	// 4) 서비스별 처리 (기존 로직 재사용)
	switch service {
	case "git-upload-pack":
		return s.handleUploadPack(conn, owner, repo)
	case "git-receive-pack":
		return s.handleReceivePack(conn, owner, repo)
	case "git-upload-archive":
		return s.handleUploadArchive(conn, owner, repo)
	default:
		return fmt.Errorf("unsupported Git service: %s", service)
	}
}

// handleUploadPack : git-upload-pack (fetch, clone) 처리
func (s *DuplexTCPServer) handleUploadPack(conn *DuplexConnection, owner, repo string) error {
	// 1) v2 광고 (ls-refs, fetch 지원)
	if err := protocol.WriteV2Advert(conn.writer, "git-executor/0.1.0", "ls-refs", "fetch"); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("flush advert: %w", err)
	}

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
			lsRefsReq, err := protocol.ParseLsRefsSection(cmds)
			if err != nil {
				return fmt.Errorf("failed to parse ls-refs section: %w", err)
			}
			// 참고: 내부에서 refs 응답 → 마지막에 flush("0000") + writer.Flush()까지 해야 함
			if err := s.handleLsRefs(conn, owner, repo, lsRefsReq); err != nil {
				return err
			}
			continue

		// ---------------------------
		// fetch (clone/pull 공용)
		// ---------------------------
		case strings.HasPrefix(cmds[0], "command=fetch"):
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

			// 저장소 열기
			pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
			if err != nil {
				return fmt.Errorf("failed to create path config: %w", err)
			}
			repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
			if err != nil {
				return fmt.Errorf("failed to get repository path: %w", err)
			}
			gitRepo, err := git.OpenRepository(repoPath)
			if err != nil {
				// 프로토콜 호환 에러 응답
				pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
				if err := pw.Err("repository not found"); err != nil {
					return fmt.Errorf("failed to write error response: %w", err)
				}
				_ = conn.writer.Flush()
				return nil
			}
			defer gitRepo.Close()

			goGitRepo, ok := gitRepo.(*git.GoGitRepository)
			if !ok {
				return fmt.Errorf("repository is not a GoGitRepository")
			}
			underlyingRepo := goGitRepo.GetGoGitRepo()

			// 전송 대상 오브젝트 계산
			objectHashes, err := protocol.BuildFetchObjectList(underlyingRepo.Storer, gitRepo, fetchReq)
			if err != nil {
				return fmt.Errorf("failed to build fetch object list: %w", err)
			}

			// === 협상 섹션 처리 ===
			// 스펙: client가 "done"을 보냈다면(clone 경로) acknowledgments 섹션은 "생략"
			//      done이 없다면(pull 경로) acknowledgments 섹션을 "반드시" 보낸 뒤 delim(0001)
			if !fetchReq.Done {
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
			}

			// === packfile 섹션 ===
			// objectHashes가 있을 때만 packfile 섹션을 열어 전송
			if len(objectHashes) > 0 {
				// "packfile" 섹션 헤더
				if err := protocol.WritePackfileSectionHeader(conn.writer); err != nil {
					return fmt.Errorf("failed to write packfile section header: %w", err)
				}

				// side-band-64k로 pack 전송 (채널 1=pack, 2=progress, 3=error)
				if err := s.writePackfileWithSideband(conn, underlyingRepo.Storer, objectHashes, fetchReq); err != nil {
					return fmt.Errorf("failed to write packfile: %w", err)
				}
			}

			// 최종 flush
			if _, err := conn.writer.WriteString("0000"); err != nil {
				return fmt.Errorf("failed to write final flush: %w", err)
			}
			if err := conn.writer.Flush(); err != nil {
				return fmt.Errorf("failed to flush writer: %w", err)
			}
			return nil
		}
	}
}

// handleReceivePack : git-receive-pack (push) 처리
func (s *DuplexTCPServer) handleReceivePack(conn *DuplexConnection, owner, repo string) error {

	// 1) Repository 열기
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	repoPath, err := repository.GetRepoPath(pathConfig, owner, repo)
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

	// 3) ref updates 읽기
	var refUpdates []string

	refUpdates, err = s.readSection(conn.reader)
	if err != nil {
		return fmt.Errorf("failed to read ref updates: %w", err)
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

	// 5) packfile 데이터 읽기 및 unpack
	scanner := packfile.NewScanner(conn.reader) // 중요: pkt-line 리더 말고, 원본 io.Reader
	parser, err := packfile.NewParserWithStorage(scanner, gitRepo.Storer)
	if err != nil {
		return err
	}

	// 6) packfile 데이터 디코드
	if _, err := parser.Parse(); err != nil {
		return err
	}

	s.logger.Info("Successfully unpacked packfile")

	// 7) 각 ref 업데이트 처리
	for _, refUpdate := range refUpdates {
		parts := strings.Fields(refUpdate)
		if len(parts) >= 3 {
			oldHash := parts[0]
			newHash := parts[1]
			refName := parts[2]

			refName = strings.TrimSuffix(refName, "\x00")

			s.logger.Info("Processing ref update",
				zap.String("old_hash", oldHash),
				zap.String("new_hash", newHash),
				zap.String("ref_name", refName))

			// ref 업데이트 수행
			if err := s.updateRef(gitRepo, oldHash, newHash, refName); err != nil {
				s.logger.Error("Failed to update ref",
					zap.String("ref_name", refName),
					zap.Error(err))

				continue
			}

		}
	}

	s.logger.Info("Push request processed successfully")
	return nil
}

// handleUploadArchive : git-upload-archive (archive) 처리
func (s *DuplexTCPServer) handleUploadArchive(conn *DuplexConnection, owner, repo string) error {

	// 0) 리포 경로 확인
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("path config: %w", err)
	}

	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		return fmt.Errorf("repo path: %w", err)
	}

	// 1) 요청 인자 읽기 (argument ... 줄들을 flush까지)
	raw, err := s.readSection(conn.reader)
	if err != nil {
		return fmt.Errorf("read upload-archive args: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("no arguments")
	}

	// 2) 인자 정규화 (argument ... 줄들을 flush까지)
	args := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "argument ")
		if line == "" {
			continue
		}
		args = append(args, line)
	}

	// 3) 인자 파싱 (최소: tree-ish 1개. 나머지는 pathspec/옵션)

	parsed, err := protocol.ParseUploadArchiveArgs(args)
	if err != nil {
		return fmt.Errorf("parse args: %w", err)
	}

	// 4) ACK + flush (프로토콜)
	if err := s.writePktLine(conn, "ACK\n"); err != nil {
		return err
	}
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("flush ACK: %w", err)
	}

	// 5) 실제 아카이브 생성 스트리밍
	// 외부 git을 이용 (권장: 호환성↑). tar/zip 등 포맷은 parsed.format 사용.
	cmdArgs := []string{"-C", repoPath, "archive"}
	if parsed.Format != "" {
		cmdArgs = append(cmdArgs, "--format="+parsed.Format)
	}
	// --prefix 등 옵션
	if parsed.Prefix != "" {
		cmdArgs = append(cmdArgs, "--prefix="+parsed.Prefix)
	}
	// tree-ish + pathspecs
	cmdArgs = append(cmdArgs, parsed.Treeish)
	cmdArgs = append(cmdArgs, parsed.Pathspecs...)

	cmd := exec.Command("git", cmdArgs...)
	// 보안상 system/global config 영향 최소화
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cannot pipe stdout: %w", err)
	}
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start archive: %w", err)
	}

	// stderr는 서버 로그로만
	go func() { io.Copy(os.Stdout, stderr) }()

	// RAW 스트림 복사 (pkt-line 아님!)
	if _, err := io.Copy(conn.writer, stdout); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("stream archive: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		// 이미 ACK/flush를 보낸 뒤라 에러 패킷을 더 보낼 수 없음 → 서버 로그만 남김
		s.logger.Error("git archive failed", zap.Error(err), zap.String("repo_path", repoPath))
		return err
	}

	// 6) 정상 종료 (EOF로 마무리)
	return nil
}

// updateRef updates a Git reference
func (s *DuplexTCPServer) updateRef(repo *gogit.Repository, oldHash, newHash, refName string) error {
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
		return nil
	}

	// Update existing reference
	// First check if the old hash matches the current reference
	currentRef, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		return fmt.Errorf("failed to get current reference %s: %w", refName, err)
	}

	if currentRef.Hash() != oldHashObj {
		return fmt.Errorf("reference %s hash mismatch: expected %s, got %s",
			refName, oldHash, currentRef.Hash().String())
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

	return nil
}

// handleLsRefs : ls-refs 명령 처리 (info/refs)
func (s *DuplexTCPServer) handleLsRefs(conn *DuplexConnection, owner, repo string, lsRefsReq *protocol.LsRefsReq) error {

	// ls-refs 응답 형식:
	// 1. capabilities (peel, symrefs 등)
	// 2. 실제 refs 목록
	// 3. flush packet

	// capabilities 전송 (클라이언트가 요청한 것만)
	capabilities := []string{}
	if lsRefsReq.Peel {
		capabilities = append(capabilities, "peel")
	}
	if lsRefsReq.Symrefs {
		capabilities = append(capabilities, "symrefs")
	}

	for _, cap := range capabilities {
		if err := s.writePktLine(conn, cap); err != nil {
			return fmt.Errorf("failed to write capability: %w", err)
		}
	}

	// 실제 repository에서 refs 가져오기
	// Create path config
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create path config: %w", err)
	}

	// Get repository path
	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
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

	// Get repository references
	refs, err := gitRepo.References()
	if err != nil {
		return fmt.Errorf("failed to get repository references: %w", err)
	}

	// List references in ls-refs format with filtering
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()

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
			hash := ref.Hash().String()
			if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") {
				// For tags with peel option, get the peeled hash (actual object hash)
				if tagObj, err := gitRepo.TagObject(ref.Hash()); err == nil && tagObj != nil {
					hash = tagObj.Target.String()
				}
			}

			// Format: <hash> <refname>
			refLine := fmt.Sprintf("%s %s", hash, refName)
			return s.writePktLine(conn, refLine)

		} else if ref.Type() == plumbing.SymbolicReference && lsRefsReq.Symrefs {
			// Symbolic reference (e.g., HEAD -> refs/heads/main)
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
	return &sidebandWriter{w: w, channel: 0x01, chunkSize: 65515}
}

func (sb *sidebandWriter) Write(p []byte) (int, error) {
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
		total += len(chunk)
	}
	return total, nil
}

func (sb *sidebandWriter) Flush() error {
	_, err := sb.w.Write([]byte("0000")) // sideband 스트림 종료
	return err
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

	// 채널 1(sideband=pack data) writer
	sbw := newSidebandWriter(conn.writer, 0x01)

	enc := packfile.NewEncoder(sbw, st, useRefDelta)

	// go-git v5: Encode(hashes []plumbing.Hash, packWindow uint)
	packWindow := uint(0) // Disable delta search for now
	if _, err := enc.Encode(hashes, packWindow); err != nil {
		return fmt.Errorf("encode pack (sideband): %w", err)
	}

	// sideband 스트림 종료
	if err := sbw.Flush(); err != nil {
		return fmt.Errorf("sideband flush: %w", err)
	}

	// bufio.Writer면 최종 flush
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

		// 5) 명령 추가
		commands = append(commands, string(payload))
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
	if err != nil {
		fmt.Printf("⛔️⛔️⛔️ ⛔️⛔️⛔️ ⛔️⛔️⛔️ ERROR: writePktLine: %v\n", err)
	}
	return err
}

// cleanupConnections : 유휴 연결 정리
func (s *DuplexTCPServer) cleanupConnections() {
	ticker := time.NewTicker(s.config.Performance.MemoryCheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for conn, duplexConn := range s.connections {
			if now.Sub(duplexConn.lastUsed) > 5*time.Minute {
				fmt.Printf("🧹 Cleaning up idle connection: %s\n", conn.RemoteAddr())
				conn.Close()
				delete(s.connections, conn)
			}
		}
		s.mu.Unlock()
	}
}

// GetConnectionCount : 활성 연결 수 반환
func (s *DuplexTCPServer) GetConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

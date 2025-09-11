package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
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
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
)

// pktLineWriter helps write Git protocol packet lines
type pktLineWriter struct {
	writer io.Writer
}

// writeLine writes a packet line in Git protocol format
func (p *pktLineWriter) writeLine(data string) error {
	// Calculate length: 4 bytes for length + data (data should NOT include \n)
	length := len(data) + 4
	line := fmt.Sprintf("%04x%s", length, data)
	_, err := p.writer.Write([]byte(line))
	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: writeLine: %v\n", line)
	return err
}

// flush writes a flush packet (0000)
func (p *pktLineWriter) flush() error {
	_, err := p.writer.Write([]byte("0000"))
	return err
}

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
	if port == 0 {
		port = 5222 // fallback to default
	}

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

// handleConnection handles a new TCP connection
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

// handleGitProtocol handles Git protocol negotiation and duplex communication
func (s *DuplexTCPServer) handleGitProtocol(conn *DuplexConnection) error {
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: handleGitProtocol\n")
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
		return s.handleReceivePack(conn)
	default:
		return fmt.Errorf("unsupported Git service: %s", service)
	}
}

// handleUploadPack handles git-upload-pack (fetch) operations
func (s *DuplexTCPServer) handleUploadPack(conn *DuplexConnection, owner, repo string) error {
	if err := protocol.WriteV2Advert(conn.writer, "git-executor/0.1.0", "ls-refs", "fetch"); err != nil {
		return fmt.Errorf("failed to send advert: %w", err)
	}
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("flush advert: %w", err)
	}
	// v2 세션을 동기 상태머신으로 처리 (duplex 루프 사용 금지)
	for {
		// 2) 섹션 하나 읽기 (flush까지)
		cmds, err := s.readV2Section(conn.reader) // pkt-line 기반
		fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: cmds: %v\n", cmds)
		if err != nil {
			return err
		}
		if len(cmds) == 0 {
			continue
		}

		switch {
		case strings.HasPrefix(cmds[0], "command=ls-refs"):
			// Parse ls-refs options using helper
			lsRefsReq, err := protocol.ParseLsRefsSection(cmds)
			if err != nil {
				return fmt.Errorf("failed to parse ls-refs section: %w", err)
			}
			if err := s.handleLsRefs(conn, owner, repo, lsRefsReq); err != nil { // pkt-line로 refs... 그리고 반드시 flush + conn.writer.Flush()
				return err
			}
			return nil // ls-refs 처리 완료 후 종료

		case strings.HasPrefix(cmds[0], "command=fetch"):
			// Fetch command detected, read the actual fetch options from next section
			fmt.Printf("🏹🏹🏹 🏹🏹🏹 🏹🏹🏹 DEBUG: cmd detected fetch: %v\n", cmds)

			// Read the actual fetch options (second section after delimiter)
			fetchCmds, err := s.readV2Section(conn.reader)
			if err != nil {
				return fmt.Errorf("failed to read fetch options: %w", err)
			}
			fmt.Printf("🏹🏹🏹 🏹🏹🏹 🏹🏹🏹 DEBUG: fetch options: %v\n", fetchCmds)

			// Parse fetch options using helper
			fetchReq, err := protocol.ParseFetchSection(fetchCmds)
			if err != nil {
				return fmt.Errorf("failed to parse fetch section: %w", err)
			}

			// Validate fetch support
			if err := protocol.ValidateFetchSupport(fetchReq); err != nil {
				return fmt.Errorf("fetch not supported: %w", err)
			}

			// Get repository for packfile generation
			pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
			if err != nil {
				return fmt.Errorf("failed to create path config: %w", err)
			}

			repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
			if err != nil {
				return fmt.Errorf("failed to get repository path: %w", err)
			}

			// Open repository using our git package
			gitRepo, err := git.OpenRepository(repoPath)
			if err != nil {
				// Send error response according to Git protocol
				pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
				if err := pw.Err("repository not found"); err != nil {
					return fmt.Errorf("failed to write error response: %w", err)
				}
				conn.writer.Flush()
				return nil
			}
			defer gitRepo.Close()

			// Get the underlying go-git repository for storer access
			goGitRepo, ok := gitRepo.(*git.GoGitRepository)
			if !ok {
				return fmt.Errorf("repository is not a GoGitRepository")
			}
			underlyingRepo := goGitRepo.GetGoGitRepo()

			// Build object list for packfile
			objectHashes, err := protocol.BuildFetchObjectList(underlyingRepo.Storer, gitRepo, fetchReq)
			if err != nil {
				return fmt.Errorf("failed to build fetch object list: %w", err)
			}

			// packfile 섹션 헤더
			if err := protocol.WritePackfileSectionHeader(conn.writer); err != nil {
				return fmt.Errorf("failed to write packfile section header: %w", err)
			}

			// Write packfile
			if err := protocol.WritePackfile(conn.writer, underlyingRepo.Storer, objectHashes); err != nil {
				return fmt.Errorf("failed to write packfile: %w", err)
			}

			conn.writer.Flush()
			return nil // 보통 fetch 후 종료
		}
	}
}

// readV2Section reads a v2 section from the reader until flush packet
func (s *DuplexTCPServer) readV2Section(reader *bufio.Reader) ([]string, error) {
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

// handleLsRefs handles ls-refs command
func (s *DuplexTCPServer) handleLsRefs(conn *DuplexConnection, owner, repo string, lsRefsReq *protocol.LsRefsReq) error {
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: handleLsRefs called with owner=%s, repo=%s, options=%+v\n", owner, repo, lsRefsReq)

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
		return conn.writer.Flush()
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

	// flush packet 전송 (0000)
	pw := &protocol.PktWriter{W: conn.writer, WithLF: false}
	if err := pw.Flush(); err != nil {
		return fmt.Errorf("failed to write flush packet: %w", err)
	}

	return conn.writer.Flush()
}

// parseFetchSection parses fetch section commands using helper
func (s *DuplexTCPServer) parseFetchSection(commands []string) (*protocol.UploadPackRequest, error) {

	// Use helper's ParseFetchSection function
	fetchReq, err := protocol.ParseFetchSection(commands)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fetch section: %w", err)
	}

	// Convert helper's FetchReq to UploadPackRequest
	req := &protocol.UploadPackRequest{
		Wants: make([]string, len(fetchReq.Wants)),
		Haves: make([]string, len(fetchReq.Haves)),
	}

	// Convert plumbing.Hash to string
	for i, want := range fetchReq.Wants {
		req.Wants[i] = want.String()
	}

	for i, have := range fetchReq.Haves {
		req.Haves[i] = have.String()
	}

	if fetchReq.Done {
	}

	return req, nil
}

// writePktLine writes a pkt-line to the connection
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

// handleReceivePack handles git-receive-pack (push) operations
func (s *DuplexTCPServer) handleReceivePack(conn *DuplexConnection) error {

	// Send Git protocol advertisement
	advertisement := "000eversion 2" +
		"0010agent=git-executor" +
		"000freceive-pack" +
		"0000"

	if _, err := conn.writer.WriteString(advertisement); err != nil {
		return fmt.Errorf("failed to send advertisement: %w", err)
	}

	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush advertisement: %w", err)
	}

	// Start duplex communication
	return s.startDuplexCommunication(conn, "receive-pack")
}

/************** pkt-line helpers **************/

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

// startDuplexCommunication starts bidirectional communication
func (s *DuplexTCPServer) startDuplexCommunication(conn *DuplexConnection, operation string) error {

	// Create channels for bidirectional communication
	clientToServer := make(chan []byte, 100)
	serverToClient := make(chan []byte, 100)

	// Start goroutines for bidirectional data flow
	var wg sync.WaitGroup

	// Client to Server (read from client, send to processing)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(clientToServer)

		buffer := make([]byte, 4096)
		for {
			n, err := conn.reader.Read(buffer)
			if err != nil {
				if err != io.EOF {
				}
				break
			}

			conn.lastUsed = time.Now()
			clientToServer <- buffer[:n]
		}
	}()

	// Server to Client (process data, send to client)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(serverToClient)

		for data := range clientToServer {
			// Process the data (here you would implement actual Git protocol logic)
			processedData := s.processGitData(conn, data, operation, conn.owner, conn.repo)

			if len(processedData) > 0 {
				serverToClient <- processedData
			}
		}
	}()

	// Send processed data to client
	wg.Add(1)
	go func() {
		defer wg.Done()

		for data := range serverToClient {
			if _, err := conn.writer.Write(data); err != nil {
				break
			}

			if err := conn.writer.Flush(); err != nil {
				break
			}
		}
	}()

	// Wait for all goroutines to complete
	wg.Wait()

	return nil
}

// processGitData processes incoming Git protocol data using upload-pack methods
func (s *DuplexTCPServer) processGitData(conn *DuplexConnection, data []byte, operation, owner, repo string) []byte {

	// Check if we should use SSH transport (you can add a flag or config option)
	useSSH := os.Getenv("USE_SSH_TRANSPORT") == "true" || true // 임시로 항상 true

	if useSSH {
		return s.processGitDataWithSSH(conn, data, operation, owner, repo)
	}

	// Use local filesystem access (existing method)
	uploadHandler, err := protocol.NewUploadPackHandler(s.config)
	if err != nil {
		return s.createErrorResponse("Failed to create upload-pack handler")
	}

	// Route to appropriate handler based on operation
	switch operation {
	case "upload-pack":
		return s.processUploadPackTCP(conn, data, owner, repo, uploadHandler)
	case "receive-pack":
		// For now, return error for receive-pack (push operations)
		return s.createErrorResponse("Receive-pack not implemented yet")
	default:
		return s.createErrorResponse("Unsupported operation: " + operation)
	}
}

// upload-pack 명령을 처리하는 함수 (upload-pack이란 Git에서 객체를 업로드(전송)하는 명령어)
func (s *DuplexTCPServer) processUploadPackTCP(conn *DuplexConnection, data []byte, owner, repo string, uploadHandler *protocol.UploadPackHandler) []byte {
	// Create path config directly
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return s.createErrorResponse("Failed to create path config")
	}

	// Get repository path
	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		return s.createErrorResponse("Invalid repository path")
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(pathConfig, repoPath); err != nil {
		return s.createErrorResponse("Repository not found")
	}

	// Open repository using go-git directly
	gogitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return s.createErrorResponse("Failed to open repository")
	}

	// Parse the upload-pack request from TCP data
	uploadReq, err := s.parseUploadPackRequestFromTCP(data)
	if err != nil {
		return s.createErrorResponse("Invalid upload-pack request")
	}

	// Create a buffer to capture the response
	var responseBuffer strings.Builder
	pktWriter := &pktLineWriter{writer: &responseBuffer}

	// Process want/have negotiation directly using Git filesystem access
	err = s.processWantHaveNegotiationDirect(conn, gogitRepo, uploadReq, pktWriter)
	if err != nil {
		return s.createErrorResponse("Failed to process negotiation")
	}

	return []byte(responseBuffer.String())
}

// parseUploadPackRequestFromTCP parses upload-pack request from TCP data
func (s *DuplexTCPServer) parseUploadPackRequestFromTCP(data []byte) (*protocol.UploadPackRequest, error) {
	req := &protocol.UploadPackRequest{
		Wants: make([]string, 0),
		Haves: make([]string, 0),
	}

	// Parse pkt-line format from TCP data
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse pkt-line format (length + data)
		if len(line) >= 4 {
			lengthStr := line[:4]
			length, err := strconv.ParseInt(lengthStr, 16, 32)
			if err != nil {
				continue // Skip invalid lines
			}

			if length == 0 {
				req.Done = true
				break
			}

			if length > 4 {
				data := line[4:]
				if strings.HasPrefix(data, "want ") {
					hash := strings.TrimSpace(data[5:])
					req.Wants = append(req.Wants, hash)
				} else if strings.HasPrefix(data, "have ") {
					hash := strings.TrimSpace(data[5:])
					req.Haves = append(req.Haves, hash)
				}
			}
		}
	}

	return req, nil
}

// processWantHaveNegotiationDirect processes want/have negotiation directly using Git filesystem
func (s *DuplexTCPServer) processWantHaveNegotiationDirect(conn *DuplexConnection, repo *gogit.Repository, req *protocol.UploadPackRequest, pktWriter *pktLineWriter) error {

	// Collect objects to send
	objectsToSend := make(map[string]*object.Commit)

	// Process each want request
	for _, wantHash := range req.Wants {
		hash := plumbing.NewHash(wantHash)
		if hash.IsZero() {
			continue
		}

		// Get commit object
		commit, err := repo.CommitObject(hash)
		if err != nil {
			continue
		}

		// Walk commit history to collect all reachable commits
		err = s.walkCommitHistory(repo, commit, objectsToSend, req.Haves)
		if err != nil {
			fmt.Errorf("failed to walk commit history: %w", err)
			return fmt.Errorf("failed to walk commit history: %w", err)
		}
	}
	// 1) 섹션 헤더 (pkt-line)
	if err := protocol.WritePackfileSectionHeader(conn.writer); err != nil {
		return fmt.Errorf("failed to write packfile section header: %w", err)
	}

	// 2) 전송할 해시 집합 계산 (want/have → 전체 객체 폐쇄)
	var wants, haves []plumbing.Hash
	for _, w := range req.Wants {
		wants = append(wants, plumbing.NewHash(w))
	}
	for _, h := range req.Haves {
		haves = append(haves, plumbing.NewHash(h))
	}

	objs, err := revlist.Objects(repo.Storer, wants, haves)
	if err != nil {
		return fmt.Errorf("revlist.Objects failed: %w", err)
	}

	// 3) RAW PACK 전송 (pkt-line 아님)
	if err := protocol.WritePackfile(conn.writer, repo.Storer, objs); err != nil {
		return fmt.Errorf("failed to encode packfile: %w", err)
	}

	// 4) bufio.Writer라면 flush
	if err := conn.writer.Flush(); err != nil {
		return fmt.Errorf("flush failed: %w", err)
	}

	return nil
}

// 커밋 히스토리 순회하며, 커밋 조각을 모으는 함수
func (s *DuplexTCPServer) walkCommitHistory(repo *gogit.Repository, commit *object.Commit, collected map[string]*object.Commit, haves []string) error {
	hash := commit.Hash.String()

	// Check if we've already processed this commit
	if _, exists := collected[hash]; exists {
		return nil
	}

	// Check if this commit is in the "have" list (client already has it)
	for _, have := range haves {
		if hash == have {
			return nil // Don't include objects the client already has
		}
	}

	// Add this commit to our collection
	collected[hash] = commit

	// Recursively walk parent commits (limit depth to prevent infinite loops)
	if len(collected) < 1000 { // Safety limit
		parents := commit.Parents()
		for {
			parent, err := parents.Next()
			if err != nil {
				break // No more parents
			}
			if err := s.walkCommitHistory(repo, parent, collected, haves); err != nil {
				return err
			}
		}
	}

	return nil
}

// processGitDataWithSSH processes Git data using go-git SSH transport
func (s *DuplexTCPServer) processGitDataWithSSH(conn *DuplexConnection, data []byte, operation, owner, repo string) []byte {
	// Parse the first few pkt-lines to determine the command
	command, err := s.parseGitCommand(data)
	if err != nil {
		return s.createErrorResponse("Failed to parse Git command")
	}

	// Route based on the actual Git command
	switch command {
	case "ls-refs":
		return s.processLsRefsWithSSH(data, owner, repo)
	case "fetch":
		return s.processUploadPackWithSSH(conn, data, owner, repo)
	default:
		return s.createErrorResponse("Unsupported Git command: " + command)
	}
}

// parseGitCommand parses the Git command from TCP data
func (s *DuplexTCPServer) parseGitCommand(data []byte) (string, error) {
	dataStr := string(data)

	// Look for actual command from client (not advertisement)
	if strings.Contains(dataStr, "command=ls-refs") {
		fmt.Printf("✅ DEBUG: Found ls-refs command\n")
		return "ls-refs", nil
	}

	// Also check for direct ls-refs command (from client advertisement)
	if strings.Contains(dataStr, "ls-refs") && !strings.Contains(dataStr, "version 2") {
		fmt.Printf("✅ DEBUG: Found ls-refs command (direct)\n")
		return "ls-refs", nil
	}

	// Look for fetch command (want/have negotiation)
	if strings.Contains(dataStr, "want ") {
		fmt.Printf("✅ DEBUG: Found fetch command\n")
		return "fetch", nil
	}

	return "", fmt.Errorf("unknown Git command")
}

// go-git을 위한 ssh
func (s *DuplexTCPServer) createSSHAuth() (transport.AuthMethod, error) {
	// Try to use SSH key from default location
	sshKeyPath := os.Getenv("HOME") + "/.ssh/id_rsa"

	// Check if key exists
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		// Try alternative key locations
		alternativePaths := []string{
			os.Getenv("HOME") + "/.ssh/id_ed25519",
			os.Getenv("HOME") + "/.ssh/id_ecdsa",
			"/etc/ssh/ssh_host_rsa_key",
		}

		for _, path := range alternativePaths {
			if _, err := os.Stat(path); err == nil {
				sshKeyPath = path
				break
			}
		}
	}

	// Read SSH private key
	sshKey, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Parse private key
	signer, err := gossh.ParsePrivateKey(sshKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	// Create SSH public key authentication
	auth := &ssh.PublicKeys{
		User:   "git",
		Signer: signer,
	}

	return auth, nil
}

// ls-refs 명령을 처리하는 함수 (ls-refs란 명령은 저장소의 모든 브랜치와 태그를 나열하는 명령)
func (s *DuplexTCPServer) processLsRefsWithSSH(data []byte, owner, repoName string) []byte {
	// Create path config
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: processLsRefsWithSSH\n")

	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return s.createErrorResponse("Failed to create path config")
	}

	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: pathConfig: %v\n", pathConfig)

	// Get repository path
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: Getting repository path\n")
	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repoName)
	if err != nil {
		return s.createErrorResponse("Invalid repository path")
	}
	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: repoPath: %v\n", repoPath)

	// Open repository
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: Opening repository\n")
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return s.createErrorResponse("Failed to open repository")
	}
	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: gitRepo: %v\n", gitRepo)

	// Get repository references
	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: Getting repository references\n")
	refs, err := gitRepo.References()
	if err != nil {
		return s.createErrorResponse("Failed to get repository references")
	}
	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: refs: %v\n", refs)

	// Create response buffer
	var responseBuffer strings.Builder
	pktWriter := &pktLineWriter{writer: &responseBuffer}
	fmt.Printf("✅✅✅ ✅✅✅ ✅✅✅ DEBUG: pktWriter: %v\n", pktWriter)

	// List references in ls-refs format
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: Ref: %v\n", ref)
			// Format: <hash> <refname>
			refLine := fmt.Sprintf("%s %s", ref.Hash().String(), ref.Name().String())
			return pktWriter.writeLine(refLine)
		}
		return nil
	})

	if err != nil {
		return s.createErrorResponse("Failed to list references")
	}

	fmt.Fprintf(pktWriter.writer, "") // no-op

	// End with flush packet
	pktWriter.flush()

	fmt.Printf("🔍🔍🔍 🔍🔍🔍 🔍🔍🔍 DEBUG: Wrote references\n")

	return []byte(responseBuffer.String())
}

// upload-pack 명령을 처리하는 함수 (upload-pack이란 Git에서 객체를 업로드(전송)하는 명령어)
func (s *DuplexTCPServer) processUploadPackWithSSH(conn *DuplexConnection, data []byte, owner, repoName string) []byte {
	// Create path config
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		return s.createErrorResponse("Failed to create path config")
	}

	// Get repository path
	repoPath, err := repository.GetAndValidateRepoPath(pathConfig, owner, repoName)
	if err != nil {
		return s.createErrorResponse("Invalid repository path")
	}

	// Open repository
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return s.createErrorResponse("Failed to open repository")
	}

	// Parse upload-pack request
	uploadReq, err := s.parseUploadPackRequestFromTCP(data)
	if err != nil {
		return s.createErrorResponse("Invalid upload-pack request")
	}

	// Create response buffer
	var responseBuffer strings.Builder
	pktWriter := &pktLineWriter{writer: &responseBuffer}

	// Process want/have negotiation
	err = s.processWantHaveNegotiationDirect(conn, gitRepo, uploadReq, pktWriter)
	if err != nil {
		return s.createErrorResponse("Failed to process negotiation")
	}

	return []byte(responseBuffer.String())
}

// receive-pack 명령을 처리하는 함수 (receive-pack이란 Git에서 객체를 수신(받는)하는 명령어)
func (s *DuplexTCPServer) processReceivePackWithSSH(gitRepo *gogit.Repository, data []byte, owner, repoName string) []byte {
	// For receive-pack, we would need to parse the incoming pack data
	// and apply it to the repository
	// This is more complex and would require implementing the full receive-pack protocol

	// For now, return a simple acknowledgment
	response := "000eversion 2\n" +
		"0010agent=git-executor-ssh\n" +
		"000freceive-pack\n" +
		"0000"

	return []byte(response)
}

// createErrorResponse creates a Git protocol error response
func (s *DuplexTCPServer) createErrorResponse(message string) []byte {
	// Git protocol error format: ERR <message>\n
	errorMsg := fmt.Sprintf("ERR %s\n", message)
	return []byte(errorMsg)
}

// cleanupConnections cleans up idle connections
func (s *DuplexTCPServer) cleanupConnections() {
	ticker := time.NewTicker(30 * time.Second)
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

// GetConnectionCount returns the number of active connections
func (s *DuplexTCPServer) GetConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

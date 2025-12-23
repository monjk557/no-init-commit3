// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// This file handles receive-pack operations (push) as the executor counterpart to upload-pack (fetch).
package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Repository lock manager for preventing concurrent pushes
var (
	repoLocks  = make(map[string]*sync.Mutex)
	locksMutex = sync.RWMutex{}
)

// ReceivePackRequest represents a parsed receive-pack request from the client
type ReceivePackRequest struct {
	Commands     []RefUpdateCommand
	Capabilities []string
	PackfileData []byte
}

// RefUpdateCommand represents a single reference update command in a push
type RefUpdateCommand struct {
	RefName string        // Reference name (e.g., refs/heads/main)
	OldHash plumbing.Hash // Current hash (zero hash for create)
	NewHash plumbing.Hash // New hash (zero hash for delete)
}

// ReceivePackHandler handles Git Smart HTTP protocol for push operations (executor side)
type ReceivePackHandler struct {
	pathConfig   *repositories.PathConfig
	errorHandler *ErrorHandler
	capabilities *ServerCapabilities
	eventsClient events.RedisStreamClient
	config       *config.Config
	// unthinFallbackFn allows tests (and future extensions) to override thin-pack
	// resolution behavior. If nil, unpackThinPackToLooseObjects is used.
	unthinFallbackFn func(repo *gogit.Repository, packfileData []byte) error
}

// NewReceivePackHandler creates a new receive-pack handler for the executor
func NewReceivePackHandler(cfg *config.Config) (*ReceivePackHandler, error) {
	// Initialize path configuration
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create error handler
	errorHandler := NewErrorHandler(nil) // Logger will be set if needed

	// Get server capabilities for receive-pack
	capabilities := DefaultServerCapabilities()

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		logger, err := logging.NewLogger(cfg.Logging)
		if err == nil {
			eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
			if err != nil {
				// Log warning but continue without events
				_ = err
			}
		}
	}

	h := &ReceivePackHandler{
		pathConfig:   pathConfig,
		errorHandler: errorHandler,
		capabilities: capabilities,
		eventsClient: eventsClient,
		config:       cfg,
	}
	h.unthinFallbackFn = h.unpackThinPackToLooseObjects
	return h, nil
}

// HandleInfoRefs handles Smart HTTP discovery for receive-pack (push).
// NOTE: In practice, many Git clients (including Apple Git variants) do not use protocol v2 for HTTP push.
// Therefore we respond with v0/v1-style advertisement for service=git-receive-pack.
func (h *ReceivePackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	service := r.URL.Query().Get("service")

	if service != "git-receive-pack" {
		http.Error(w, "Invalid service parameter", http.StatusBadRequest)
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}
	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
		return
	}

	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = repoWrapper.Close() }()

	goGitRepo := repoWrapper.GetRepo()

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Smart HTTP service announcement
	pktWriter := &pktLineWriter{w: w}
	_ = pktWriter.writeLine("# service=git-receive-pack")
	_ = pktWriter.flush()

	// v1 ref advertisement + capabilities
	caps := []string{
		"report-status",
		"delete-refs",
		// IMPORTANT:
		// Some clients (notably Apple Git) may still send thin packs even if we
		// don't advertise "thin-pack". Advertising "no-thin" explicitly tells the
		// client that thin packs are NOT acceptable, forcing a complete pack.
		"no-thin",
		// NOTE:
		// - side-band-64k를 광고하면 클라이언트가 sideband 모드로 응답을 해석하는데,
		//   이 구현은 (progress/sideband 멀티플렉싱 없이) report-status pkt-line만 반환한다.
		// - thin-pack도 광고하지 않아 클라이언트가 thin pack을 보내지 않도록 유도한다.
		// 필요 시 기능을 단계적으로 추가.
	}
	_ = WriteV1Advert(w, *goGitRepo, caps...)
}

func (h *ReceivePackHandler) HandleReceivePack(w http.ResponseWriter, r *http.Request) {
	// [1] HTTP request 수신 (POST /git-receive-pack)
	ctx := r.Context()

	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
		return
	}

	repoLock := getRepositoryLock(repoPath)
	repoLock.Lock()
	defer repoLock.Unlock()

	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = repoWrapper.Close() }()

	gitRepo := repoWrapper.GetRepo()

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")

	w.WriteHeader(http.StatusOK)

	pktWriter := &pktLineWriter{w: w}

	// v2 vs v1 감지:
	// - v2: 첫 섹션 첫 라인이 "command=" 로 시작
	// - v1: "old-oid new-oid refname[\\x00cap...]" 형태의 update 커맨드로 시작
	//
	// body를 peek하여 판단 (그리고 파싱을 위해 bytes로 보관)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
	peekLen := 200
	if len(bodyBytes) < peekLen {
		peekLen = len(bodyBytes)
	}

	// body를 다시 읽을 수 있도록 복원
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	cmds, err := h.readSection(r.Body)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}

	if len(cmds) == 0 {
		_ = pktWriter.flush()
		return
	}

	// v1 fallback: 첫 라인이 command=가 아니면 v1 receive-pack로 처리
	if !strings.HasPrefix(cmds[0], "command=") {
		receiveReq, err := h.parseV1ReceivePack(bodyBytes)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.flush()
			return
		}
		if err := h.processReceivePackRequest(ctx, gitRepo, receiveReq, w, owner, repo, pktWriter); err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.flush()
			return
		}
		return
	}

	if strings.HasPrefix(cmds[0], "command=ls-refs") {
		// [4] command=ls-refs 처리
		// ls-refs options 섹션 읽기
		lsRefsOpts, err := h.readSection(r.Body)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}
		allCmds := append(cmds, lsRefsOpts...)
		lsRefsReq, err := ParseLsRefsSection(allCmds)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		// refs 설명 생성 및 전송
		if err := h.handleLsRefs(ctx, gitRepo, lsRefsReq, pktWriter); err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		// [6] flush
		_ = pktWriter.flush()
		// [7] 종료
		return
	} else if !strings.HasPrefix(cmds[0], "command=push") {
		_ = pktWriter.writeLine("ERR unknown command")
		return
	}

	// [4] command=push 처리
	// push 옵션 섹션 읽기
	pushOpts, err := h.readSection(r.Body)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
	allCmds := append(cmds, pushOpts...)

	// v2 push 섹션에서 ref-update 명령과 packfile 파싱
	receiveReq, err := h.parsePushSection(allCmds, r.Body)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}

	// [4] receive-pack 처리 시작
	err = h.processReceivePackRequest(ctx, gitRepo, receiveReq, w, owner, repo, pktWriter)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
}

func (h *ReceivePackHandler) readSection(body io.Reader) ([]string, error) {
	var cmds []string
	buf := make([]byte, 4)

	for {
		n, err := io.ReadFull(body, buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return cmds, err
		}
		if n != 4 {
			break
		}

		lengthHex := string(buf)
		length, err := strconv.ParseInt(lengthHex, 16, 32)
		if err != nil {
			continue
		}

		// flush-pkt: end of this message/section
		if length == 0 {
			break
		}

		// delim-pkt: end of this section (v2 request framing)
		if length == 1 {
			break
		}

		if length < 4 {
			continue
		}

		dataLen := int(length) - 4
		if dataLen <= 0 {
			continue
		}

		dataBuf := make([]byte, dataLen)
		n, err = io.ReadFull(body, dataBuf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return cmds, err
		}
		if n != dataLen {
			continue
		}

		data := strings.TrimSuffix(string(dataBuf), "\n")
		cmds = append(cmds, data)
	}

	return cmds, nil
}

// parseV1ReceivePack parses v1 receive-pack request from raw HTTP body bytes.
// Format:
// - pkt-line framed update commands
// - flush-pkt
// - raw packfile (optional)
func (h *ReceivePackHandler) parseV1ReceivePack(body []byte) (*ReceivePackRequest, error) {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	rd := bytes.NewReader(body)
	lenBuf := make([]byte, 4)
	first := true

	for {
		if _, err := io.ReadFull(rd, lenBuf); err != nil {
			if err == io.EOF {
				return req, nil
			}
			return nil, err
		}

		n, err := strconv.ParseInt(string(lenBuf), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid pkt-line length: %w", err)
		}
		if n == 0 {
			// flush: remainder is packfile
			remaining, _ := io.ReadAll(rd)
			if len(remaining) > 0 {
				req.PackfileData = remaining
			}
			return req, nil
		}
		if n < 4 {
			return nil, fmt.Errorf("invalid pkt-line length: %d", n)
		}

		dataLen := int(n) - 4
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(rd, data); err != nil {
			return nil, err
		}

		line := strings.TrimSuffix(string(data), "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}

		// First line may contain capabilities after NUL
		var capsPart string
		if first && strings.Contains(line, "\x00") {
			parts := strings.SplitN(line, "\x00", 2)
			line = parts[0]
			capsPart = parts[1]
		}
		if first && capsPart != "" {
			req.Capabilities = append(req.Capabilities, strings.Fields(capsPart)...)
		}
		first = false

		// "old new ref"
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		oldHash := plumbing.NewHash(fields[0])
		newHash := plumbing.NewHash(fields[1])
		refName := fields[2]
		req.Commands = append(req.Commands, RefUpdateCommand{
			RefName: refName,
			OldHash: oldHash,
			NewHash: newHash,
		})
	}
}

func (h *ReceivePackHandler) parsePushSection(cmds []string, body io.Reader) (*ReceivePackRequest, error) {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	var currentOldOid, currentNewOid, currentRefName string

	for _, cmd := range cmds {
		c := strings.TrimSpace(cmd)
		if c == "" {
			continue
		}

		switch {
		case c == "command=push":
		case strings.HasPrefix(c, "agent="):
		case strings.HasPrefix(c, "old-oid "):
			// [6] ref-update 명령 수신
			currentOldOid = strings.TrimSpace(c[8:])
		case strings.HasPrefix(c, "new-oid "):
			currentNewOid = strings.TrimSpace(c[8:])
		case strings.HasPrefix(c, "ref-name "):
			currentRefName = strings.TrimSpace(c[9:])
			if currentOldOid != "" && currentNewOid != "" && currentRefName != "" {
				oldHash := plumbing.NewHash(currentOldOid)
				newHash := plumbing.NewHash(currentNewOid)

				cmd := RefUpdateCommand{
					RefName: currentRefName,
					OldHash: oldHash,
					NewHash: newHash,
				}
				req.Commands = append(req.Commands, cmd)
				currentOldOid = ""
				currentNewOid = ""
				currentRefName = ""
			}
		default:
		}
	}

	// [8] packfile 수신 시작
	// [9] packfile 헤더 수신
	// [10] packfile 스트리밍 수신
	// [11] request body EOF 수신
	// [12] packfile 수신 완료
	// v2에서는 ref-update 명령 이후 packfile이 raw binary로 전송됨
	remaining, err := io.ReadAll(body)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("error reading packfile: %w", err)
	}

	if len(remaining) > 0 {
		// packfile은 "PACK"으로 시작하는 raw binary 데이터
		req.PackfileData = remaining
	}

	return req, nil
}

func (h *ReceivePackHandler) processReceivePackRequest(ctx context.Context, repo *gogit.Repository, req *ReceivePackRequest, w http.ResponseWriter, owner, repoName string, pktWriter *pktLineWriter) error {
	// NOTE:
	// receive-pack의 "side-band-64k"는 진행(progress) 메시지 다중화 용도인데,
	// 이 구현은 우선 v1 report-status(pkt-line)를 정확히 반환하는 것을 목표로 한다.
	// (side-band로 pkt-line 자체를 감싸면 클라이언트가 "remote: 000eunpack ok"처럼 그대로 출력하고,
	// status 파싱이 꼬일 수 있음)
	if pktWriter == nil {
		pktWriter = newPktLineWriter(w)
	} else {
		pktWriter.w = w
	}

	if len(req.Commands) == 0 {
		if err := pktWriter.writeLine("unpack ok"); err != nil {
			return fmt.Errorf("failed to write unpack status: %w", err)
		}
		return pktWriter.flush()
	}

	if len(req.PackfileData) > 0 {
		// [13] unpack 시작
		err := h.processPackfile(ctx, repo, req.PackfileData)
		if err != nil {
			if err := pktWriter.writeLine(fmt.Sprintf("unpack %s", err.Error())); err != nil {
				return fmt.Errorf("failed to write unpack error: %w", err)
			}
			return pktWriter.flush()
		}
	}

	if err := pktWriter.writeLine("unpack ok"); err != nil {
		return fmt.Errorf("failed to write unpack status: %w", err)
	}

	// [15] refs 업데이트
	for _, cmd := range req.Commands {
		err := h.processRefUpdate(ctx, repo, cmd, owner, repoName)
		if err != nil {
			// [17] report-status 생성
			statusLine := fmt.Sprintf("ng %s %s", cmd.RefName, err.Error())
			if err := pktWriter.writeLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref error status: %w", err)
			}
		} else {
			// [17] report-status 생성
			statusLine := fmt.Sprintf("ok %s", cmd.RefName)
			if err := pktWriter.writeLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref success status: %w", err)
			}
		}
	}

	// [18] progress / status 전송 (side-band)
	// [19] 최종 flush
	if err := pktWriter.flush(); err != nil {
		return err
	}

	// [20] HTTP response body 종료
	// [21] 모든 처리 완료
	return nil
}

func (h *ReceivePackHandler) processPackfile(ctx context.Context, repo *gogit.Repository, packfileData []byte) error {
	if len(packfileData) == 0 {
		return nil
	}

	header, err := h.validatePackfileFormat(packfileData)
	if err != nil {
		return fmt.Errorf("invalid packfile format: %w", err)
	}

	if !bytes.HasPrefix(packfileData, []byte("PACK")) {
		return fmt.Errorf("invalid packfile signature")
	}

	// IMPORTANT:
	// Do NOT use packfile.NewParserWithStorage(scanner, repo.Storer) here.
	// When a storage is provided, go-git’s parser may try to read non-delta base
	// objects from the storage during delta resolution. For filesystem storage,
	// base objects are written via LazyWriter but are only closed at the end of
	// Parse() (deferred), which can surface as "object not found" during Parse.
	//
	// The correct server-side behavior is to persist the received pack as a
	// pack+idx via storer.PackfileWriter. That path uses a parser WITHOUT storage.
	pw, ok := repo.Storer.(storer.PackfileWriter)
	if !ok {
		// Fallback: at least validate the packfile parses (without storage)
		reader := bytes.NewReader(packfileData)
		scanner := packfile.NewScanner(reader)
		parser, err := packfile.NewParser(scanner)
		if err != nil {
			return fmt.Errorf("failed to create packfile parser: %w", err)
		}
		if _, err := parser.Parse(); err != nil {
			if err == packfile.ErrReferenceDeltaNotFound {
				return fmt.Errorf("failed to parse packfile: %w (thin pack or missing base objects)", err)
			}
			return fmt.Errorf("failed to parse packfile: %w", err)
		}
		_ = header.ObjectCount
		return nil
	}

	w, err := pw.PackfileWriter()
	if err != nil {
		return fmt.Errorf("failed to open packfile writer: %w", err)
	}

	if _, err := io.Copy(w, bytes.NewReader(packfileData)); err != nil {
		_ = w.Close()
		return fmt.Errorf("failed to write packfile: %w", err)
	}
	if err := w.Close(); err != nil {
		// NOTE:
		// Apple Git 등 일부 클라이언트는 서버가 thin-pack을 광고하지 않아도
		// stateless-rpc(push)에서 억지로 thin pack을 보내는 경우가 있습니다.
		// 이때 PackfileWriter 경로(내부적으로 parser without storage)는
		// external REF_DELTA(base outside pack)를 resolve할 수 없어 여기로 떨어집니다.
		//
		// 서버측에서는 git index-pack --fix-thin과 유사하게, repo에 이미 존재하는
		// base object를 사용해 delta를 적용해 “완성된 객체”를 저장하면 push를 받아줄 수 있습니다.
		// 따라서 ErrReferenceDeltaNotFound이면 loose object 언팩(Delta 적용)으로 fallback합니다.
		if errors.Is(err, packfile.ErrReferenceDeltaNotFound) {
			unthinFn := h.unthinFallbackFn
			if unthinFn == nil {
				unthinFn = h.unpackThinPackToLooseObjects
			}
			if thinErr := unthinFn(repo, packfileData); thinErr == nil {
				_ = header.ObjectCount
				return nil
			} else {
				return fmt.Errorf("failed to parse packfile: %w (thin pack or missing base objects)", err)
			}
		}
		return fmt.Errorf("failed to persist packfile: %w", err)
	}

	_ = header.ObjectCount
	return nil
}

// unpackThinPackToLooseObjects는 thin pack(REF_DELTA base가 pack 외부)에 대해
// repo에 이미 존재하는 base object를 찾아 delta를 적용한 뒤, 결과 객체를 loose object로 저장합니다.
//
// - 장점: 외부 git 커맨드 없이도 Apple Git의 thin push를 수용 가능
// - 단점: pack+idx로 저장하지 않고 loose로 저장(추후 gc/repack 필요)
func (h *ReceivePackHandler) unpackThinPackToLooseObjects(repo *gogit.Repository, packfileData []byte) error {
	reader := bytes.NewReader(packfileData)
	scanner := packfile.NewScanner(reader)

	// Header()를 호출해 scanner 상태를 pack 시작으로 맞춤
	_, objCount, err := scanner.Header()
	if err != nil {
		return err
	}

	type deltaEntry struct {
		offset     int64
		baseOffset int64
		baseHash   plumbing.Hash
		isOFS      bool
		deltaBytes []byte
	}

	offsetToHash := make(map[int64]plumbing.Hash, 256)
	pending := make(map[int64]*deltaEntry, 64)
	var order []int64

	// 1차 패스: pack을 순서대로(헤더의 object count만큼) 읽으며
	// - non-delta: 즉시 저장(SetEncodedObject)하고 offset->hash 기록
	// - delta: deltaBytes만 보관(pending)
	for i := uint32(0); i < objCount; i++ {
		oh, err := scanner.NextObjectHeader()
		if err != nil {
			return err
		}

		var buf bytes.Buffer
		if _, _, err := scanner.NextObject(&buf); err != nil {
			return err
		}

		switch oh.Type {
		case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
			// 해시 계산(정확한 git object hash)
			hasher := plumbing.NewHasher(oh.Type, oh.Length)
			_, _ = hasher.Write(buf.Bytes())
			objHash := hasher.Sum()

			obj := repo.Storer.NewEncodedObject()
			obj.SetType(oh.Type)
			obj.SetSize(oh.Length)
			w, err := obj.Writer()
			if err != nil {
				return err
			}
			if _, err := w.Write(buf.Bytes()); err != nil {
				_ = w.Close()
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
			if _, err := repo.Storer.SetEncodedObject(obj); err != nil {
				return err
			}

			// sanity: 저장된 해시와 계산된 해시가 다르면 이상하지만 진행은 가능
			offsetToHash[oh.Offset] = objHash

		case plumbing.OFSDeltaObject:
			e := &deltaEntry{
				offset:     oh.Offset,
				baseOffset: oh.OffsetReference,
				isOFS:      true,
				deltaBytes: append([]byte(nil), buf.Bytes()...),
			}
			pending[oh.Offset] = e
			order = append(order, oh.Offset)

		case plumbing.REFDeltaObject:
			e := &deltaEntry{
				offset:     oh.Offset,
				baseHash:   oh.Reference,
				isOFS:      false,
				deltaBytes: append([]byte(nil), buf.Bytes()...),
			}
			pending[oh.Offset] = e
			order = append(order, oh.Offset)

		default:
			return fmt.Errorf("unsupported pack object type: %s", oh.Type)
		}
	}

	// resolve helper
	resolveOne := func(e *deltaEntry) (bool, error) {
		if _, ok := offsetToHash[e.offset]; ok {
			delete(pending, e.offset)
			return true, nil
		}

		var base plumbing.Hash
		if e.isOFS {
			// OFS는 base가 pack 내 앞쪽 오프셋에 존재해야 함
			// base가 delta라면 먼저 resolve되어야 하므로 없으면 "아직"
			bh, ok := offsetToHash[e.baseOffset]
			if !ok {
				return false, nil
			}
			base = bh
		} else {
			base = e.baseHash
		}

		// base object가 repo에 존재해야 delta 적용 가능(REF thin의 핵심)
		baseObj, err := repo.Storer.EncodedObject(plumbing.AnyObject, base)
		if err != nil {
			// REF delta의 base가 같은 pack 안에 있는 경우, base가 아직 저장되지 않았을 수 있으니
			// 한 번 더 루프에서 기회를 줌.
			return false, nil
		}

		target := repo.Storer.NewEncodedObject()
		target.SetType(baseObj.Type())
		if err := packfile.ApplyDelta(target, baseObj, e.deltaBytes); err != nil {
			return false, err
		}

		if _, err := repo.Storer.SetEncodedObject(target); err != nil {
			return false, err
		}
		offsetToHash[e.offset] = target.Hash()
		delete(pending, e.offset)
		return true, nil
	}

	// 2차 패스: delta 해소(의존성 때문에 여러 번 돌 수 있음)
	for len(pending) > 0 {
		progress := false

		for _, off := range order {
			e, ok := pending[off]
			if !ok {
				continue
			}
			ok2, err := resolveOne(e)
			if err != nil {
				return err
			}
			if ok2 {
				progress = true
			}
		}

		if !progress {
			// 더 이상 base를 찾지 못함 = 진짜 missing base
			return packfile.ErrReferenceDeltaNotFound
		}
	}

	return nil
}

type PackfileHeader struct {
	Version     uint32
	ObjectCount uint32
}

func (h *ReceivePackHandler) validatePackfileFormat(packfileData []byte) (*PackfileHeader, error) {
	if len(packfileData) < 12 {
		return nil, fmt.Errorf("packfile too short: need at least 12 bytes, got %d", len(packfileData))
	}

	// Check packfile signature (first 4 bytes should be "PACK")
	signature := string(packfileData[0:4])
	if signature != "PACK" {
		return nil, fmt.Errorf("invalid packfile signature: expected 'PACK', got '%s'", signature)
	}

	// Read version (bytes 4-7, big endian)
	version := binary.BigEndian.Uint32(packfileData[4:8])
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported packfile version: %d", version)
	}

	// Read object count (bytes 8-11, big endian)
	objectCount := binary.BigEndian.Uint32(packfileData[8:12])

	return &PackfileHeader{
		Version:     version,
		ObjectCount: objectCount,
	}, nil
}

func (h *ReceivePackHandler) processRefUpdate(ctx context.Context, repo *gogit.Repository, cmd RefUpdateCommand, owner, repoName string) error {
	// [7] refs 검증 / fast-forward 검사
	if !isValidRefName(cmd.RefName) {
		return fmt.Errorf("invalid reference name: %s", cmd.RefName)
	}

	ref, err := repo.Reference(plumbing.ReferenceName(cmd.RefName), true)
	if err != nil && err != plumbing.ErrReferenceNotFound {
		return fmt.Errorf("failed to get reference %s: %w", cmd.RefName, err)
	}

	isCreate := cmd.OldHash == plumbing.ZeroHash
	isDelete := cmd.NewHash == plumbing.ZeroHash

	if !isCreate {
		if ref == nil {
			return fmt.Errorf("reference %s does not exist", cmd.RefName)
		}
		if ref.Hash() != cmd.OldHash {
			return fmt.Errorf("reference %s has been modified (expected %s, got %s)", cmd.RefName, cmd.OldHash, ref.Hash())
		}
	}

	if !isDelete {
		_, err := repo.CommitObject(cmd.NewHash)
		if err != nil {
			_, err = repo.TagObject(cmd.NewHash)
			if err != nil {
				_, err = repo.Object(plumbing.AnyObject, cmd.NewHash)
				if err != nil {
					return fmt.Errorf("object %s not found in repository", cmd.NewHash)
				}
			}
		}
	}

	if isDelete {
		err = repo.Storer.RemoveReference(plumbing.ReferenceName(cmd.RefName))
		if err != nil {
			return fmt.Errorf("failed to delete reference %s: %w", cmd.RefName, err)
		}
	} else {
		newRef := plumbing.NewHashReference(plumbing.ReferenceName(cmd.RefName), cmd.NewHash)
		err = repo.Storer.SetReference(newRef)
		if err != nil {
			return fmt.Errorf("failed to update reference %s: %w", cmd.RefName, err)
		}

		// [16] hooks 실행 (pre-receive / update / post-receive)
		if cmd.OldHash != cmd.NewHash && strings.HasPrefix(cmd.RefName, "refs/heads/") {
			oldHashStr := ""
			if cmd.OldHash != plumbing.ZeroHash {
				oldHashStr = cmd.OldHash.String()
			}
			h.addCommitEventToStream(ctx, repo, oldHashStr, cmd.NewHash, cmd.RefName, owner, repoName, events.EventTypePush)
		}
	}

	return nil
}

func isValidRefName(refName string) bool {
	if refName == "" {
		return false
	}

	if !strings.HasPrefix(refName, "refs/") {
		return false
	}

	for _, r := range refName {
		if r >= 0 && r <= 31 {
			return false
		}
	}

	invalidChars := []string{" ", "~", "^", ":", "?", "*", "[", "\\", "..", "//", "@{"}
	for _, char := range invalidChars {
		if strings.Contains(refName, char) {
			return false
		}
	}

	if strings.HasSuffix(refName, "/") || strings.HasSuffix(refName, ".lock") {
		return false
	}

	return true
}

func getRepositoryLock(repoPath string) *sync.Mutex {
	locksMutex.RLock()
	lock, exists := repoLocks[repoPath]
	locksMutex.RUnlock()

	if exists {
		return lock
	}

	// Create new lock
	locksMutex.Lock()
	defer locksMutex.Unlock()

	// Double-check in case another goroutine created it
	if lock, exists := repoLocks[repoPath]; exists {
		return lock
	}

	lock = &sync.Mutex{}
	repoLocks[repoPath] = lock
	return lock
}

func (h *ReceivePackHandler) addCommitEventToStream(ctx context.Context, repo *gogit.Repository, oldHash string, commitHash plumbing.Hash, refName, owner, repoName string, eventType events.CommitEventType) {
	if h.eventsClient == nil {
		return
	}

	// Get commit object to verify it exists
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		// Not a commit, skip
		return
	}

	// Get full filesystem path for the repository
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		// Skip if we can't get the path
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, h.pathConfig.GetRepoRoot())

	// Create event
	event := &events.CommitEvent{
		RepoPath:   relativePath,
		EventType:  eventType,
		CommitHash: commitHash.String(),
		OldRev:     oldHash,
		RefName:    refName,
		Pusher:     "git", // Default pusher identifier
		Timestamp:  commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			// Log error but don't fail the operation
			_ = err
		}
	}()
}

func (h *ReceivePackHandler) handleLsRefs(ctx context.Context, repo *gogit.Repository, lsRefsReq *LsRefsReq, pktWriter *pktLineWriter) error {
	// [4] refs 설명 생성
	refs, err := repo.References()
	if err != nil {
		return fmt.Errorf("failed to get references: %w", err)
	}
	defer refs.Close()

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

	if headRequested {
		headRef, err := repo.Reference(plumbing.HEAD, true)
		if err == nil {
			if headRef.Type() == plumbing.SymbolicReference && lsRefsReq.Symrefs {
				target := headRef.Target().String()
				if err := pktWriter.writeLine(fmt.Sprintf("@%s HEAD", target)); err != nil {
					return err
				}
			} else if headRef.Type() == plumbing.HashReference {
				hash := headRef.Hash().String()
				if err := pktWriter.writeLine(fmt.Sprintf("%s HEAD", hash)); err != nil {
					return err
				}
			}
		}
	}

	// [5] refs pkt-line 전송
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()

		if refName == "HEAD" {
			return nil
		}

		if len(lsRefsReq.RefPrefix) > 0 {
			matched := false
			for _, prefix := range lsRefsReq.RefPrefix {
				if strings.HasPrefix(refName, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}

		if ref.Type() == plumbing.HashReference {
			hash := ref.Hash().String()

			if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") {
				if tagObj, err := repo.TagObject(ref.Hash()); err == nil && tagObj != nil {
					peeledHash := tagObj.Target.String()
					if err := pktWriter.writeLine(fmt.Sprintf("%s %s", hash, refName)); err != nil {
						return err
					}
					return pktWriter.writeLine(fmt.Sprintf("peeled %s", peeledHash))
				}
			}

			return pktWriter.writeLine(fmt.Sprintf("%s %s", hash, refName))
		} else if ref.Type() == plumbing.SymbolicReference && lsRefsReq.Symrefs {
			target := ref.Target().String()
			return pktWriter.writeLine(fmt.Sprintf("@%s %s", target, refName))
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to list references: %w", err)
	}

	return nil
}

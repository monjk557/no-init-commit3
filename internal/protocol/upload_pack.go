// Package protocol - Git Smart HTTP 프로토콜 핸들러 구현
// collabops-git-http 게이트웨이로부터 전달받은 요청을 처리하고
// go-git를 사용하여 핵심 Git 프로토콜 로직을 구현함
package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
)

// UploadPackRequest - 클라이언트로부터 파싱된 upload-pack 요청
type UploadPackRequest struct {
	Wants        []string // 클라이언트가 원하는 객체 ID들
	Haves        []string // 클라이언트가 이미 가진 객체 ID들
	Capabilities []string // 클라이언트 기능 목록
	Done         bool     // 요청 완료 플래그
}

// pktLineWriter - Git 프로토콜 패킷 라인 작성 헬퍼
type pktLineWriter struct {
	w io.Writer
}

// sidebandWriter - side-band-64k 포맷으로 데이터를 작성하는 writer
// side-band 포맷: [밴드번호][데이터]를 pkt-line으로 감싸서 전송
// 밴드 번호: 0x01=pack data, 0x02=progress, 0x03=error
type sidebandWriter struct {
	w          io.Writer
	flusher    http.Flusher // HTTP flusher for streaming responses
	channel    byte         // 1=pack, 2=progress, 3=fatal
	chunkSize  int          // payload(채널 포함) 최대 길이 (64k - overhead)
	firstWrite bool         // Track if this is the first write (for immediate flush)
}

// newSidebandWriter creates a new sideband writer for 64k chunks
func newSidebandWriter(w io.Writer, channel byte) *sidebandWriter {
	// pkt-line 총 길이(4바이트 헤더 포함) 최대 65520
	// payload = length-4, 그 중 1바이트는 채널이 차지
	// 안전하게 payload(채널 포함) 65515 사용
	sbw := &sidebandWriter{
		w:          w,
		channel:    channel,
		chunkSize:  65515,
		firstWrite: true,
	}
	// Try to get http.Flusher if w is an http.ResponseWriter
	if flusher, ok := w.(http.Flusher); ok {
		sbw.flusher = flusher
	}
	return sbw
}

// Write implements io.Writer for sidebandWriter
// 데이터를 64k 청크로 나눠서 side-band 포맷으로 전송
func (sb *sidebandWriter) Write(p []byte) (int, error) {
	fmt.Printf("[upload-pack] [side-band] Write called with %d bytes\n", len(p))

	if len(p) == 0 {
		fmt.Printf("[upload-pack] [side-band] Write: empty data, returning 0\n")
		return 0, nil
	}

	chunksWritten := 0
	total := 0
	originalLen := len(p)

	for len(p) > 0 {
		n := sb.chunkSize - 1 // 채널 바이트(1)를 뺀 실제 데이터 공간
		if n > len(p) {
			n = len(p)
		}
		chunk := p[:n]
		p = p[n:]

		payloadLen := 1 + len(chunk) // [channel][data...]
		length := payloadLen + 4     // pkt-line 총 길이

		fmt.Printf("[upload-pack] [side-band] Writing chunk: length=%d (hex: %04x), payload=%d, channel=0x%02x, data_len=%d\n", length, length, payloadLen, sb.channel, len(chunk))

		if _, err := fmt.Fprintf(sb.w, "%04x", length); err != nil {
			fmt.Printf("[upload-pack] [side-band] ERROR: Failed to write length header: %v\n", err)
			return total, err
		}
		if _, err := sb.w.Write([]byte{sb.channel}); err != nil {
			fmt.Printf("[upload-pack] [side-band] ERROR: Failed to write channel byte: %v\n", err)
			return total, err
		}
		if _, err := sb.w.Write(chunk); err != nil {
			fmt.Printf("[upload-pack] [side-band] ERROR: Failed to write data chunk: %v\n", err)
			return total, err
		}

		// Flush HTTP response writer to ensure data is sent immediately
		// This is critical for the first packet containing "PACK" header
		if sb.flusher != nil {
			if sb.firstWrite {
				// First write must be flushed immediately so client receives "PACK" header
				fmt.Printf("[upload-pack] [side-band] First write, flushing immediately\n")
				sb.flusher.Flush()
				sb.firstWrite = false
			} else {
				// For subsequent writes, flush periodically (every packet for small packets)
				// This ensures streaming behavior
				sb.flusher.Flush()
			}
		}

		chunksWritten++
		total += len(chunk)
	}

	fmt.Printf("[upload-pack] [side-band] Write completed: wrote %d chunks, total %d/%d bytes (channel=0x%02x)\n", chunksWritten, total, originalLen, sb.channel)
	return total, nil
}

// Flush writes the flush packet to end sideband stream
func (sb *sidebandWriter) Flush() error {
	fmt.Printf("[upload-pack] [side-band] Flushing (sending 0000)\n")
	_, err := sb.w.Write([]byte("0000"))
	if err != nil {
		fmt.Printf("[upload-pack] [side-band] ERROR: Flush failed: %v\n", err)
	} else {
		fmt.Printf("[upload-pack] [side-band] Flush completed\n")
	}
	return err
}

// UploadPackHandler - Git fetch 작업을 위한 Smart HTTP 프로토콜 핸들러 (git clone/pull)
type UploadPackHandler struct {
	pathConfig *repositories.PathConfig // 리포지토리 경로 설정
}

// NewUploadPackHandler - 새 upload-pack 핸들러 생성
func NewUploadPackHandler(cfg *config.Config) (*UploadPackHandler, error) {
	// 리포지토리 경로 설정 초기화
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &UploadPackHandler{
		pathConfig: pathConfig,
	}, nil
}

// HandleInfoRefs - GET /repos/:owner/:repo/info/refs?service=git-upload-pack 처리
// 리포지토리의 참조(refs)와 기능을 게이트웨이로 전달 (클라이언트에게 전달됨)
func (h *UploadPackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 게이트웨이로부터 전달받은 경로 파라미터 추출
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	service := r.URL.Query().Get("service")

	// 서비스 파라미터 검증
	if service != "git-upload-pack" {
		http.Error(w, "Invalid service parameter", http.StatusBadRequest)
		return
	}

	// Get repository path and validate
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Open repository using go-git with simple caching
	gitRepo, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = gitRepo.Close() }()

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Write service advertisement
	_, _ = fmt.Fprintf(w, "001e# service=git-upload-pack\n")

	// Write empty line (required by Git protocol)
	// Empty line is 5 bytes total: 4 bytes for length header + 1 byte for newline
	_, _ = fmt.Fprintf(w, "0000")

	// Get repository references using our git package
	// For now, we'll implement a basic version that gets HEAD and branches
	err = h.writeReferences(ctx, w, gitRepo)
	if err != nil {
		// If we've already started writing, we can't send an HTTP error
		// Log the error and write an error packet instead
		_, _ = fmt.Fprintf(w, "0000") // end packet
		return
	}

	// End with null packet (only once, at the very end)
	_, _ = fmt.Fprintf(w, "0000")
}

// writeReferences writes Git references in the Smart HTTP protocol format
func (h *UploadPackHandler) writeReferences(ctx context.Context, w http.ResponseWriter, repo *repositories.RepositoryWrapper) error {
	// This is a simplified implementation. In a full implementation,
	// we would use go-git to get all references and format them properly
	// For now, we'll implement basic functionality to get started

	// Try to get some basic repository information
	// We'll enhance this as we develop the git package further

	// Get commits to find HEAD reference
	commits, err := commits.GetCommits(repo, ctx, "HEAD", 1, 0)
	if err != nil {
		// Repository might be empty or HEAD might not exist
		// Write a dummy reference to satisfy the protocol
		line := "0000000000000000000000000000000000000000 capabilities^{}\x00multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor\n"
		pktLine := fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))
		return nil
	}

	if len(commits) > 0 {
		// Write HEAD reference with capabilities
		headHash := commits[0].Sha
		line := fmt.Sprintf("%s HEAD\x00multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor\n", headHash)
		pktLine := fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))

		// Write main branch reference (without capabilities)
		line = fmt.Sprintf("%s refs/heads/main\n", headHash)
		pktLine = fmt.Sprintf("%04x%s", len(line)+4, line)
		_, _ = w.Write([]byte(pktLine))
	}

	return nil
}

// HandleUploadPack handles POST /repos/:owner/:repo/git-upload-pack
// This endpoint processes want/have negotiation and streams packfiles to the gateway
func (h *UploadPackHandler) HandleUploadPack(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[upload-pack] [1/7] HandleUploadPack: Started\n")
	ctx := r.Context()

	// Extract path parameters from gateway-forwarded request
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	fmt.Printf("[upload-pack] [2/7] HandleUploadPack: Extracted params owner=%s, repo=%s\n", owner, repo)

	// Get user ID from gateway context (forwarded header)
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		fmt.Printf("[upload-pack] ERROR: Missing user authentication context\n")
		http.Error(w, "Missing user authentication context", http.StatusUnauthorized)
		return
	}
	fmt.Printf("[upload-pack] [3/7] HandleUploadPack: User ID=%s\n", userID)

	// Get repository path and validate
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: Invalid repository path: %v\n", err)
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}
	fmt.Printf("[upload-pack] [4/7] HandleUploadPack: Repository path=%s\n", repoPath)

	// Ensure repository exists
	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		fmt.Printf("[upload-pack] ERROR: Repository not found: %v\n", err)
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Parse the upload-pack request from client (forwarded by gateway)
	fmt.Printf("[upload-pack] [5/7] HandleUploadPack: Parsing request body\n")
	uploadReq, err := h.parseUploadPackRequest(r.Body)
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: Failed to parse request: %v\n", err)
		http.Error(w, fmt.Sprintf("Invalid upload-pack request: %v", err), http.StatusBadRequest)
		return
	}
	fmt.Printf("[upload-pack] [5/7] HandleUploadPack: Parsed request - wants=%d, haves=%d, capabilities=%v\n", len(uploadReq.Wants), len(uploadReq.Haves), uploadReq.Capabilities)

	// Open repository using go-git
	fmt.Printf("[upload-pack] [6/7] HandleUploadPack: Opening repository\n")
	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: Failed to open repository: %v\n", err)
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}

	gitRepo := gitRepoWrapper.GetRepo()
	defer func() { _ = gitRepoWrapper.Close() }()

	// Set response headers for Git Smart HTTP protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// Enable flushing for streaming responses
	if flusher, ok := w.(http.Flusher); ok {
		defer flusher.Flush()
		fmt.Printf("[upload-pack] [6/7] HandleUploadPack: HTTP Flusher enabled\n")
	}

	// Create a packet line writer
	pktWriter := &pktLineWriter{w: w}

	// Process want/have negotiation
	fmt.Printf("[upload-pack] [7/7] HandleUploadPack: Starting negotiation and packfile generation\n")
	err = h.processWantHaveNegotiation(ctx, gitRepo, uploadReq, pktWriter)
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: Negotiation failed: %v\n", err)
		// If we've started writing, we can't send HTTP error anymore
		// Write an error packet instead
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
	fmt.Printf("[upload-pack] [DONE] HandleUploadPack: Completed successfully\n")
}

// parseUploadPackRequest parses the upload-pack request body in pkt-line format
func (h *UploadPackHandler) parseUploadPackRequest(body io.Reader) (*UploadPackRequest, error) {
	req := &UploadPackRequest{
		Wants: make([]string, 0),
		Haves: make([]string, 0),
	}

	buf := make([]byte, 4) // Buffer for reading length header
	firstWant := true

	for {
		// Read 4-byte length header
		n, err := io.ReadFull(body, buf)
		if err != nil {
			if err == io.EOF {
				break // End of input
			}
			return req, err
		}
		if n != 4 {
			break
		}

		// Parse length
		lengthHex := string(buf)
		length, err := strconv.ParseInt(lengthHex, 16, 32)
		if err != nil {
			continue // Skip invalid packet
		}

		if length == 0 {
			// Flush packet (0000)
			continue
		}

		if length < 4 {
			// Invalid packet length
			continue
		}

		// Read the data portion (length - 4 for the header we already read)
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
			return req, err
		}
		if n != dataLen {
			continue
		}

		// Convert to string and trim newline if present
		data := strings.TrimSuffix(string(dataBuf), "\n")

		if strings.HasPrefix(data, "want ") {
			parts := strings.Fields(data)
			if len(parts) >= 2 {
				req.Wants = append(req.Wants, parts[1])

				// Capabilities are sent with the first want line
				if firstWant && len(parts) > 2 {
					req.Capabilities = parts[2:]
					firstWant = false
				}
			}
		} else if strings.HasPrefix(data, "have ") {
			parts := strings.Fields(data)
			if len(parts) >= 2 {
				req.Haves = append(req.Haves, parts[1])
			}
		} else if strings.TrimSpace(data) == "done" {
			req.Done = true
			break
		}
	}

	return req, nil
}

// hasSideBand64k checks if the request has side-band-64k capability
func (h *UploadPackHandler) hasSideBand64k(caps []string) bool {
	for _, cap := range caps {
		if cap == "side-band-64k" {
			return true
		}
	}
	return false
}

// processWantHaveNegotiation handles the want/have negotiation and packfile generation
func (h *UploadPackHandler) processWantHaveNegotiation(ctx context.Context, repo *gogit.Repository, req *UploadPackRequest, pktWriter *pktLineWriter) error {
	fmt.Printf("[upload-pack] [negotiation-1/4] Starting negotiation\n")

	if len(req.Wants) == 0 {
		fmt.Printf("[upload-pack] ERROR: No wants specified\n")
		return fmt.Errorf("no wants specified")
	}

	// Check if all wanted objects exist
	fmt.Printf("[upload-pack] [negotiation-2/4] Validating %d wanted objects\n", len(req.Wants))
	for _, want := range req.Wants {
		hash := plumbing.NewHash(want)
		_, err := repo.Object(plumbing.AnyObject, hash)
		if err != nil {
			fmt.Printf("[upload-pack] ERROR: Object %s not found: %v\n", want, err)
			return fmt.Errorf("object %s not found: %w", want, err)
		}
	}
	fmt.Printf("[upload-pack] [negotiation-2/4] All wanted objects validated\n")

	useSideBand := h.hasSideBand64k(req.Capabilities)
	fmt.Printf("[upload-pack] [negotiation-3/4] Side-band-64k: %v\n", useSideBand)

	// Send NAK (no acknowledgment)
	// Note: NAK is part of negotiation phase, so it must be sent as regular pkt-line,
	// NOT in side-band format. Side-band is only used for packfile data.
	fmt.Printf("[upload-pack] [negotiation-3/4] Sending NAK (regular pkt-line)\n")
	if err := pktWriter.writeLine("NAK"); err != nil {
		fmt.Printf("[upload-pack] ERROR: Failed to write NAK: %v\n", err)
		return fmt.Errorf("failed to write NAK: %w", err)
	}

	// Flush to end negotiation phase
	fmt.Printf("[upload-pack] [negotiation-3/4] Flushing negotiation phase\n")
	if err := pktWriter.flush(); err != nil {
		fmt.Printf("[upload-pack] ERROR: Failed to flush negotiation: %v\n", err)
		return fmt.Errorf("failed to flush negotiation: %w", err)
	}

	// Generate and stream packfile
	fmt.Printf("[upload-pack] [negotiation-4/4] Starting packfile generation (useSideBand=%v)\n", useSideBand)
	err := h.generateAndStreamPackfile(ctx, repo, req, pktWriter, useSideBand)
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: Packfile generation failed: %v\n", err)
		return fmt.Errorf("failed to generate packfile: %w", err)
	}

	fmt.Printf("[upload-pack] [negotiation-DONE] Negotiation completed successfully\n")
	return nil
}

// generateAndStreamPackfile generates and streams a packfile for the requested objects
func (h *UploadPackHandler) generateAndStreamPackfile(ctx context.Context, repo *gogit.Repository, req *UploadPackRequest, pktWriter *pktLineWriter, useSideBand bool) error {
	fmt.Printf("[upload-pack] [packfile-1/7] Starting packfile generation\n")

	// Get repository storer for packfile encoding
	storer := repo.Storer

	// Convert wants and haves to plumbing.Hash
	fmt.Printf("[upload-pack] [packfile-2/7] Converting wants/haves to hashes\n")
	fmt.Printf("[upload-pack] [packfile-2/7] req.Wants count: %d\n", len(req.Wants))
	wantHashes := make([]plumbing.Hash, 0, len(req.Wants))
	for i, want := range req.Wants {
		fmt.Printf("[upload-pack] [packfile-2/7] Converting want[%d]: %s\n", i, want)
		wantHashes = append(wantHashes, plumbing.NewHash(want))
	}
	fmt.Printf("[upload-pack] [packfile-2/7] Want hashes: %d\n", len(wantHashes))

	fmt.Printf("[upload-pack] [packfile-2/7] req.Haves count: %d\n", len(req.Haves))
	haveHashes := make([]plumbing.Hash, 0, len(req.Haves))
	for i, have := range req.Haves {
		fmt.Printf("[upload-pack] [packfile-2/7] Converting have[%d]: %s\n", i, have)
		haveHashes = append(haveHashes, plumbing.NewHash(have))
	}
	fmt.Printf("[upload-pack] [packfile-2/7] Have hashes: %d\n", len(haveHashes))

	// Use go-git's revlist.Objects to calculate which objects to send
	// This automatically handles commits, trees, blobs, and excludes objects the client already has
	fmt.Printf("[upload-pack] [packfile-3/7] About to call revlist.Objects with %d wants, %d haves\n", len(wantHashes), len(haveHashes))
	if len(wantHashes) > 0 {
		fmt.Printf("[upload-pack] [packfile-3/7] First want hash: %s\n", wantHashes[0].String())
	}
	fmt.Printf("[upload-pack] [packfile-3/7] Calculating objects to send using revlist.Objects\n")
	fmt.Printf("[upload-pack] [packfile-3/7] storer is nil: %v\n", storer == nil)
	fmt.Printf("[upload-pack] [packfile-3/7] Calling revlist.Objects NOW...\n")
	objectHashes, err := revlist.Objects(storer, wantHashes, haveHashes)
	fmt.Printf("[upload-pack] [packfile-3/7] revlist.Objects returned, err=%v, objects=%d\n", err, len(objectHashes))
	os.Stdout.Sync() // Force flush
	if err != nil {
		fmt.Printf("[upload-pack] ERROR: revlist.Objects failed: %v\n", err)
		os.Stdout.Sync()
		return fmt.Errorf("failed to calculate objects to send: %w", err)
	}
	fmt.Printf("[upload-pack] [packfile-3/7] Objects to send: %d\n", len(objectHashes))
	os.Stdout.Sync()
	fmt.Printf("[upload-pack] [packfile-3/7] Checking if objects is empty...\n")
	os.Stdout.Sync()

	if len(objectHashes) == 0 {
		fmt.Printf("[upload-pack] [packfile-4/7] No objects to send, sending empty packfile\n")
		os.Stdout.Sync()
		// No objects to send, just send empty packfile
		return h.sendEmptyPackfile(pktWriter.w, useSideBand)
	}
	fmt.Printf("[upload-pack] [packfile-4/7] Objects found, proceeding to create packfile\n")
	os.Stdout.Sync()

	// Determine if we should use ref-delta or ofs-delta
	useRefDelta := true
	for _, cap := range req.Capabilities {
		if cap == "ofs-delta" {
			useRefDelta = false
			break
		}
	}
	fmt.Printf("[upload-pack] [packfile-4/7] Use ref-delta: %v\n", useRefDelta)

	// Create writer for packfile
	fmt.Printf("[upload-pack] [packfile-5/7] Creating packfile writer (useSideBand=%v)\n", useSideBand)

	// Get the underlying HTTP ResponseWriter from HandleUploadPack for flushing
	// We need to pass it through the context or get it differently
	// For now, try to extract it from pktWriter.w if possible
	var httpWriter io.Writer = pktWriter.w
	var sbWriter *sidebandWriter
	if useSideBand {
		// Try to get http.ResponseWriter from pktWriter.w
		// pktWriter.w should be the http.ResponseWriter passed to HandleUploadPack
		fmt.Printf("[upload-pack] [packfile-5/7] Checking if writer is http.ResponseWriter...\n")
		if httpW, ok := pktWriter.w.(http.ResponseWriter); ok {
			fmt.Printf("[upload-pack] [packfile-5/7] Writer is http.ResponseWriter, using it for side-band\n")
			httpWriter = httpW
		} else {
			fmt.Printf("[upload-pack] [packfile-5/7] Warning: Writer is not http.ResponseWriter, flusher may not work\n")
		}
		// Use side-band writer for 64k chunks
		sbWriter = newSidebandWriter(httpWriter, 0x01) // channel 1 = pack data
		fmt.Printf("[upload-pack] [packfile-5/7] Created side-band writer (channel=0x01, hasFlusher=%v)\n", sbWriter.flusher != nil)
	} else {
		fmt.Printf("[upload-pack] [packfile-5/7] Using regular writer (no side-band)\n")
	}

	// Create packfile encoder
	fmt.Printf("[upload-pack] [packfile-6/7] Creating packfile encoder\n")
	var packWriter io.Writer
	if useSideBand {
		packWriter = sbWriter
	} else {
		packWriter = pktWriter.w
	}
	enc := packfile.NewEncoder(packWriter, storer, useRefDelta)

	// Encode packfile with the collected objects
	// packWindow of 0 disables delta search for simplicity
	packWindow := uint(0)
	fmt.Printf("[upload-pack] [packfile-6/7] Encoding packfile with %d objects (packWindow=%d)\n", len(objectHashes), packWindow)

	// Before encoding, log first few object hashes for debugging
	if len(objectHashes) > 0 {
		maxLog := 5
		if len(objectHashes) < maxLog {
			maxLog = len(objectHashes)
		}
		fmt.Printf("[upload-pack] [packfile-6/7] First %d object hashes: ", maxLog)
		for i := 0; i < maxLog; i++ {
			fmt.Printf("%s ", objectHashes[i].String()[:8])
		}
		fmt.Printf("\n")
	}

	fmt.Printf("[upload-pack] [packfile-6/7] About to call enc.Encode with %d objects, packWindow=%d\n", len(objectHashes), packWindow)

	// Call enc.Encode with panic recovery
	var encodeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[upload-pack] ERROR: Panic in enc.Encode: %v\n", r)
				encodeErr = fmt.Errorf("panic in packfile encoding: %v", r)
			}
		}()
		_, encodeErr = enc.Encode(objectHashes, packWindow)
	}()

	if encodeErr != nil {
		fmt.Printf("[upload-pack] ERROR: Packfile encoding failed: %v\n", encodeErr)
		return fmt.Errorf("failed to encode packfile: %w", encodeErr)
	}
	fmt.Printf("[upload-pack] [packfile-6/7] Packfile encoding completed\n")

	// Flush side-band stream if using side-band
	fmt.Printf("[upload-pack] [packfile-7/7] Flushing packfile stream\n")
	if useSideBand {
		fmt.Printf("[upload-pack] [packfile-7/7] Flushing side-band stream\n")
		if err := sbWriter.Flush(); err != nil {
			fmt.Printf("[upload-pack] ERROR: Side-band flush failed: %v\n", err)
			return fmt.Errorf("failed to flush side-band stream: %w", err)
		}
		fmt.Printf("[upload-pack] [packfile-7/7] Side-band stream flushed (sent 0000)\n")
	} else {
		// For non-side-band, send flush packet
		fmt.Printf("[upload-pack] [packfile-7/7] Flushing regular pkt-line\n")
		if err := pktWriter.flush(); err != nil {
			fmt.Printf("[upload-pack] ERROR: Regular flush failed: %v\n", err)
			return fmt.Errorf("failed to flush: %w", err)
		}
		fmt.Printf("[upload-pack] [packfile-7/7] Regular flush completed (sent 0000)\n")
	}

	fmt.Printf("[upload-pack] [packfile-DONE] Packfile generation completed successfully\n")
	return nil
}

// collectTreeObjects recursively collects blob and tree objects from a tree
func (h *UploadPackHandler) collectTreeObjects(tree *object.Tree, collected map[plumbing.Hash]bool) error {
	for _, entry := range tree.Entries {
		collected[entry.Hash] = true

		if entry.Mode == filemode.Dir {
			// This is a subdirectory, recurse into it
			subTree, err := tree.Tree(entry.Name)
			if err == nil {
				err = h.collectTreeObjects(subTree, collected)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// sendEmptyPackfile sends an empty packfile (for repositories with no objects to send)
func (h *UploadPackHandler) sendEmptyPackfile(w io.Writer, useSideBand bool) error {
	fmt.Printf("[upload-pack] [empty-packfile] Sending empty packfile (useSideBand=%v)\n", useSideBand)
	// Pack header: "PACK" + version(4 bytes) + object count(4 bytes)
	packHeader := []byte{
		'P', 'A', 'C', 'K', // PACK magic
		0x00, 0x00, 0x00, 0x02, // version 2
		0x00, 0x00, 0x00, 0x00, // object count = 0
	}

	// Pack trailer: SHA-1 of pack (for empty pack, this is constant)
	// SHA-1 of empty pack with version 2 and 0 objects
	packTrailer := []byte{
		0xda, 0x39, 0xa3, 0xee, 0x5e, 0x6b, 0x4b, 0x0d,
		0x32, 0x55, 0xbf, 0xef, 0x95, 0x60, 0x18, 0x90,
		0xaf, 0xd8, 0x07, 0x09,
	}

	if useSideBand {
		sbWriter := newSidebandWriter(w, 0x01)
		if _, err := sbWriter.Write(packHeader); err != nil {
			return err
		}
		if _, err := sbWriter.Write(packTrailer); err != nil {
			return err
		}
		return sbWriter.Flush()
	}

	if _, err := w.Write(packHeader); err != nil {
		return err
	}
	if _, err := w.Write(packTrailer); err != nil {
		return err
	}
	_, err := w.Write([]byte("0000"))
	return err
}

// writeLine writes a packet line with the data
func (p *pktLineWriter) writeLine(data string) error {
	// Handle empty data as flush packet (Git protocol compliance)
	if data == "" {
		return p.flush()
	}

	// Add newline if not present (for normal Git protocol lines)
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}

	// Calculate packet length (4 bytes for length + data)
	length := len(data) + 4

	// Check maximum packet line length (Git protocol limit)
	if length > 65520 {
		return fmt.Errorf("packet line too long: %d bytes (max 65520)", length)
	}

	line := fmt.Sprintf("%04x%s", length, data)

	_, err := p.w.Write([]byte(line))
	return err
}

// flush writes a flush packet (0000)
func (p *pktLineWriter) flush() error {
	_, err := p.w.Write([]byte("0000"))
	return err
}

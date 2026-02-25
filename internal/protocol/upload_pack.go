package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

type UploadPackRequest struct {
	Wants        []string
	Haves        []string
	Capabilities []string
	Done         bool
}

type pktLineWriter struct {
	w io.Writer
}

type sidebandWriter struct {
	w         io.Writer
	channel   byte
	chunkSize int
}

func newSidebandWriter(w io.Writer, channel byte) *sidebandWriter {
	return &sidebandWriter{
		w:         w,
		channel:   channel,
		chunkSize: 65515,
	}
}

func (sb *sidebandWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	total := 0

	for len(p) > 0 {
		n := sb.chunkSize - 1
		if n > len(p) {
			n = len(p)
		}
		chunk := p[:n]
		p = p[n:]

		payloadLen := 1 + len(chunk)
		length := payloadLen + 4

		lengthHex := fmt.Sprintf("%04x", length)
		if len(lengthHex) != 4 {
			return total, fmt.Errorf("invalid pkt-line length: %s", lengthHex)
		}

		if _, err := sb.w.Write([]byte(lengthHex)); err != nil {
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
	_, err := sb.w.Write([]byte("0000"))
	return err
}

type UploadPackHandler struct {
	pathConfig *repositories.PathConfig
}

func NewUploadPackHandler(cfg *config.Config) (*UploadPackHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &UploadPackHandler{
		pathConfig: pathConfig,
	}, nil
}

func (h *UploadPackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	service := r.URL.Query().Get("service")
	// [2] 서비스 라인 수신 (git-upload-pack)

	if service != "git-upload-pack" {
		http.Error(w, "Invalid service parameter", http.StatusBadRequest)
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	gitRepo, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}
	defer func() { _ = gitRepo.Close() }()

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// [5] protocol v2 광고 전송
	// Advertise shallow so clients can send "deepen N" (e.g. clone --depth 1).
	if err := WriteV2Advert(w, "git/collabops-git-executor", "ls-refs", "fetch=shallow"); err != nil {
		return
	}
}

func (h *UploadPackHandler) HandleUploadPack(w http.ResponseWriter, r *http.Request) {
	// [1] HTTP request 수신
	ctx := r.Context()

	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open repository: %v", err), http.StatusInternalServerError)
		return
	}

	gitRepo := gitRepoWrapper.GetRepo()
	defer func() { _ = gitRepoWrapper.Close() }()

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")

	w.WriteHeader(http.StatusOK)

	pktWriter := &pktLineWriter{w: w}

	// [2] version=2 확인
	// [3] command=ls-refs 파싱
	cmds, err := h.readSection(r.Body)
	if err != nil {
		_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}

	if len(cmds) == 0 {
		// protocol v2: empty-request = flush-pkt (클라이언트가 더 이상 요청하지 않음을 표시)
		_ = pktWriter.flush()
		return
	}

	// v2 protocol: command=ls-refs 또는 command=fetch
	if strings.HasPrefix(cmds[0], "command=ls-refs") {
		// v2 framing:
		//   command + capability-list (until delim-pkt=0001)
		//   command-args (until flush-pkt=0000)
		// ls-refs options 섹션(=command-args) 읽기
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

		// [4] refs 설명 생성
		// [5] refs pkt-line 전송
		if err := h.handleLsRefs(ctx, gitRepo, lsRefsReq, pktWriter); err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		// [6] 0000
		_ = pktWriter.flush()
		// [7] 종료
		return
	} else if strings.HasPrefix(cmds[0], "command=fetch") {
		// fetch 처리 (v2)
		// fetch options 섹션(=command-args) 읽기
		fetchOpts, err := h.readSection(r.Body)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}
		allCmds := append(cmds, fetchOpts...)
		fetchReq, err := ParseFetchSection(allCmds)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		// MVP 정책: 지원하지 않는 옵션은 ERR로 반환 (packfile 섹션 시작 전)
		if err := ValidateFetchSupport(fetchReq); err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.flush()
			return
		}

		fetchResult, err := BuildFetchObjectList(gitRepo.Storer, gitRepo, fetchReq)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.flush()
			return
		}

		uploadReq := &UploadPackRequest{
			Wants: make([]string, 0),
			Haves: make([]string, 0),
		}
		for _, want := range fetchReq.Wants {
			uploadReq.Wants = append(uploadReq.Wants, want.String())
		}
		for _, have := range fetchReq.Haves {
			uploadReq.Haves = append(uploadReq.Haves, have.String())
		}
		if fetchReq.OfsDelta {
			uploadReq.Capabilities = append(uploadReq.Capabilities, "ofs-delta")
		}

		// v2 fetch 응답:
		// - 클라이언트가 'done'을 보낸 경우: acknowledgments 섹션은 MUST omit, 바로 packfile 섹션
		// - 'done'이 없는 경우: acknowledgments + ready 보낸 뒤 같은 응답 스트림에서 곧바로 packfile 전송
		//   (return 하면 HTTP 응답이 끝나 클라이언트가 "expected packfile to be sent after 'ready'" 오류 발생)
		if !fetchReq.Done {
			// acknowledgments section
			if err := pktWriter.writeLine("acknowledgments"); err != nil {
				_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.flush()
				return
			}

			ackSent := false
			for _, have := range fetchReq.Haves {
				h := have
				if _, err := gitRepo.Object(plumbing.AnyObject, h); err == nil {
					if err := pktWriter.writeLine(fmt.Sprintf("ACK %s", h.String())); err != nil {
						_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
						_ = pktWriter.flush()
						return
					}
					ackSent = true
				}
			}
			if !ackSent {
				if err := pktWriter.writeLine("NAK"); err != nil {
					_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
					_ = pktWriter.flush()
					return
				}
			}
			// 'ready' 후 같은 연결에서 packfile 전송 (flush/return 제거)
			_ = pktWriter.writeLine("ready")
		}

		// shallow-info 섹션 (deepen N 사용 시)
		if len(fetchResult.ShallowBoundary) > 0 {
			if err := pktWriter.writeLine("shallow-info"); err != nil {
				_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.flush()
				return
			}
			for _, h := range fetchResult.ShallowBoundary {
				if err := pktWriter.writeLine("shallow " + h.String()); err != nil {
					_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
					_ = pktWriter.flush()
					return
				}
			}
			if err := pktWriter.writeDelim(); err != nil {
				_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.flush()
				return
			}
		}

		// packfile section header (PKT-LINE("packfile" LF))
		if err := pktWriter.writeLine("packfile"); err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.flush()
			return
		}

		// v2에서는 packfile 데이터 전송은 pkt-line sideband(채널 1)로 전송 (raw PACK 금지)
		flusher, hasFlusher := w.(http.Flusher)
		err = h.generateAndStreamPackfileV2(ctx, gitRepo, fetchResult.Objects, fetchReq.OfsDelta, w)
		if err != nil {
			_ = pktWriter.writeLine(fmt.Sprintf("ERR %s", err.Error()))
			if hasFlusher {
				flusher.Flush()
			}
			return
		}

		if hasFlusher {
			flusher.Flush()
		}
		return
	} else {
		_ = pktWriter.writeLine("ERR unknown command")
		return
	}
}

func (h *UploadPackHandler) readSection(body io.Reader) ([]string, error) {
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

func (h *UploadPackHandler) generateAndStreamPackfileV2(ctx context.Context, repo *gogit.Repository, objectHashes []plumbing.Hash, ofsDelta bool, httpWriter http.ResponseWriter) error {
	storer := repo.Storer

	if len(objectHashes) == 0 {
		// v2에서는 항상 sideband 사용
		return h.sendEmptyPackfile(httpWriter, true)
	}

	useRefDelta := !ofsDelta

	// v2에서는 항상 sideband 사용 (raw PACK 절대 금지)
	// [15] side-band writer 생성 (channel 0x01 = pack data)
	sbWriter := newSidebandWriter(httpWriter, 0x01)
	// [16] packfile encoder 생성
	enc := packfile.NewEncoder(sbWriter, storer, useRefDelta)
	// [12] packfile 헤더 전송

	// [14] packfile 인코딩 시작
	packWindow := uint(DefaultPackWindow)
	var encodeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				encodeErr = fmt.Errorf("panic in packfile encoding: %v", r)
			}
		}()
		// [17] packfile chunk 전송
		_, encodeErr = enc.Encode(objectHashes, packWindow)
	}()
	// [18] packfile 인코딩 완료

	if encodeErr != nil {
		return fmt.Errorf("failed to encode packfile: %w", encodeErr)
	}

	// [19] side-band flush
	if err := sbWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush side-band stream: %w", err)
	}
	// [21] 최종 flush
	if flusher, ok := httpWriter.(http.Flusher); ok {
		flusher.Flush()
	}

	// [22] HTTP response body 종료
	// [23] 모든 처리 완료
	return nil
}

func (h *UploadPackHandler) handleLsRefs(ctx context.Context, repo *gogit.Repository, lsRefsReq *LsRefsReq, pktWriter *pktLineWriter) error {
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
		// protocol v2 ls-refs: "<obj-id> HEAD [symref-target:<target>]" (unborn 미지원)
		headRef, err := repo.Reference(plumbing.HEAD, false)
		if err == nil {
			var (
				headOID   string
				headAttrs []string
			)

			switch headRef.Type() {
			case plumbing.HashReference:
				headOID = headRef.Hash().String()
			case plumbing.SymbolicReference:
				if lsRefsReq.Symrefs {
					target := headRef.Target().String()
					headAttrs = append(headAttrs, fmt.Sprintf("symref-target:%s", target))
					// obj-id는 가능하면 resolve된 타깃 OID 사용 (unborn은 광고하지 않으므로 생략)
					if targetRef, err := repo.Reference(headRef.Target(), true); err == nil && targetRef.Type() == plumbing.HashReference {
						headOID = targetRef.Hash().String()
					} else {
						// resolve 불가(unborn 등): 이 구현에서는 unborn 기능을 광고하지 않으므로 HEAD 라인 생략
						headOID = ""
					}
				}
			}

			if headOID != "" {
				line := fmt.Sprintf("%s HEAD", headOID)
				if len(headAttrs) > 0 {
					line += " " + strings.Join(headAttrs, " ")
				}
				if err := pktWriter.writeLine(line); err != nil {
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

			attrs := make([]string, 0, 2)

			// peel: "peeled:<oid>" 를 같은 라인에 attribute로 포함
			if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") {
				if tagObj, err := repo.TagObject(ref.Hash()); err == nil && tagObj != nil {
					attrs = append(attrs, fmt.Sprintf("peeled:%s", tagObj.Target.String()))
				}
			}

			line := fmt.Sprintf("%s %s", hash, refName)
			if len(attrs) > 0 {
				line += " " + strings.Join(attrs, " ")
			}
			return pktWriter.writeLine(line)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to list references: %w", err)
	}

	return nil
}

func (h *UploadPackHandler) sendEmptyPackfile(w io.Writer, useSideBand bool) error {
	packHeader := []byte{
		'P', 'A', 'C', 'K',
		0x00, 0x00, 0x00, 0x02,
		0x00, 0x00, 0x00, 0x00,
	}

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
	return nil
}

func (p *pktLineWriter) writeLine(data string) error {
	if data == "" {
		return p.flush()
	}

	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}

	length := len(data) + 4

	if length > 65520 {
		return fmt.Errorf("packet line too long: %d bytes (max 65520)", length)
	}

	line := fmt.Sprintf("%04x%s", length, data)
	_, err := p.w.Write([]byte(line))
	return err
}

func (p *pktLineWriter) flush() error {
	_, err := p.w.Write([]byte("0000"))
	return err
}

// writeDelim writes the v2 delim-pkt (0001) to separate sections.
func (p *pktLineWriter) writeDelim() error {
	_, err := p.w.Write([]byte("0001"))
	return err
}

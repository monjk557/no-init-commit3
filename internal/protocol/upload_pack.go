package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
)

type UploadPackRequest struct {
	Wants        []string
	Haves        []string
	Capabilities []string
	Done         bool
}

// Use optimized versions for better performance
type pktLineWriter = performance.OptimizedPktLineWriter
type sidebandWriter = performance.OptimizedSidebandWriter

func newSidebandWriter(w io.Writer, channel byte) *sidebandWriter {
	return performance.NewOptimizedSidebandWriter(w, channel)
}

// Removed: Now using optimized versions from performance package

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

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// protocol v2 advertisement with shallow + filter support
	if err := WriteV2Advert(w, "git/collabops-git-executor", "ls-refs", "fetch=shallow filter"); err != nil {
		return
	}
}

func (h *UploadPackHandler) HandleUploadPack(w http.ResponseWriter, r *http.Request) {
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

	// Body를 response 시작 전에 먼저 읽는다.
	// 이유: libcurl(Apple Git 등)은 body > 1024 bytes일 때 Expect: 100-continue를 보내는데,
	// WriteHeader(200)을 먼저 호출하면 Go HTTP 서버가 100 Continue 대신 200을 보내고,
	// 클라이언트 Transport가 body 전송을 취소해서 body가 비어버린다.
	cmds, err := h.readSection(r.Body)
	if err != nil {
		fmt.Printf("[DEBUG upload-pack] readSection error: %v\n", err)
		http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
		return
	}

	fmt.Printf("[DEBUG upload-pack] first section cmds(%d): %v\n", len(cmds), cmds)

	// 명령어에 따라 추가 body section도 미리 읽는다
	var lsRefsReq *LsRefsReq
	var fetchReq *FetchReq
	if len(cmds) == 0 {
		fmt.Printf("[DEBUG upload-pack] empty cmds, returning flush. Content-Length=%d, Transfer-Encoding=%s\n",
			r.ContentLength, r.Header.Get("Transfer-Encoding"))
	} else if strings.HasPrefix(cmds[0], "command=ls-refs") {
		lsRefsOpts, err := h.readSection(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
			return
		}
		allCmds := append(cmds, lsRefsOpts...)
		lsRefsReq, err = ParseLsRefsSection(allCmds)
		if err != nil {
			http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
			return
		}
	} else if strings.HasPrefix(cmds[0], "command=fetch") {
		fetchOpts, err := h.readSection(r.Body)
		if err != nil {
			fmt.Printf("[DEBUG upload-pack] fetch readSection error: %v\n", err)
			http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
			return
		}
		fmt.Printf("[DEBUG upload-pack] fetch opts(%d): wants=%d, has_done=%v\n",
			len(fetchOpts),
			countPrefix(fetchOpts, "want "),
			containsStr(fetchOpts, "done"))
		allCmds := append(cmds, fetchOpts...)
		fetchReq, err = ParseFetchSection(allCmds)
		if err != nil {
			fmt.Printf("[DEBUG upload-pack] ParseFetchSection error: %v\n", err)
			http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
			return
		}
	}

	// Body 읽기 완료 — 이제 response를 시작한다
	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	w.WriteHeader(http.StatusOK)

	pktWriter := performance.NewOptimizedPktLineWriter(w)

	if len(cmds) == 0 {
		_ = pktWriter.Flush()
		return
	}

	// v2 protocol: command=ls-refs or command=fetch
	if lsRefsReq != nil {
		if err := handleLsRefsNative(ctx, repoPath, lsRefsReq, pktWriter); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		_ = pktWriter.Flush()
		return
	} else if fetchReq != nil {
		fmt.Printf("[DEBUG upload-pack] fetchReq: wants=%d, haves=%d, done=%v, deepen=%d\n",
			len(fetchReq.Wants), len(fetchReq.Haves), fetchReq.Done, fetchReq.Deepen)

		if err := ValidateFetchSupport(fetchReq); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.Flush()
			return
		}

		// Negotiation: acknowledgments + ready, then fall through to packfile
		// Per Git protocol v2 spec, when server sends "ready" it MUST continue
		// with packfile in the same response stream (delimiter 0001, not flush 0000).
		// Filter haves to only those that exist on the server.
		// Clients may send OIDs that were force-pushed away or GC'd on the server;
		// passing unknown OIDs to pack-objects causes "fatal: bad object".
		var knownHaves []string
		if !fetchReq.Done {
			if err := pktWriter.WriteLine("acknowledgments"); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.Flush()
				return
			}

			for _, have := range fetchReq.Haves {
				if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
					if err := pktWriter.WriteLine(fmt.Sprintf("ACK %s", have)); err != nil {
						_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
						_ = pktWriter.Flush()
						return
					}
					knownHaves = append(knownHaves, have)
				}
			}
			if len(knownHaves) == 0 {
				if err := pktWriter.WriteLine("NAK"); err != nil {
					_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
					_ = pktWriter.Flush()
					return
				}
			}
			if err := pktWriter.WriteLine("ready"); err != nil {
				_ = pktWriter.Flush()
				return
			}
			// Delimiter (0001) separates acknowledgments section from packfile section.
			// Do NOT use Flush (0000) here — that would end the response and hang the client.
			if err := pktWriter.WriteDelim(); err != nil {
				_ = pktWriter.Flush()
				return
			}
			// Fall through to packfile transmission below
		} else {
			// Client sent "done" — still filter haves for pack-objects
			for _, have := range fetchReq.Haves {
				if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
					knownHaves = append(knownHaves, have)
				}
			}
		}

		// Compute shallow graft commits for deepen requests.
		// These OIDs are used in both shallow-info (sent to client) and
		// pack-objects stdin (--shallow lines to limit traversal).
		var shallowGrafts []string
		if fetchReq.Deepen > 0 {
			shallowGrafts, err = GetShallowBoundary(repoPath, fetchReq)
			if err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR shallow boundary: %s", err.Error()))
				_ = pktWriter.Flush()
				return
			}
		}

		// Shallow-info section
		if len(shallowGrafts) > 0 {
			if err := pktWriter.WriteLine("shallow-info"); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.Flush()
				return
			}
			for _, h := range shallowGrafts {
				if err := pktWriter.WriteLine("shallow " + h); err != nil {
					_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
					_ = pktWriter.Flush()
					return
				}
			}
			if err := pktWriter.WriteDelim(); err != nil {
				_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
				_ = pktWriter.Flush()
				return
			}
		}

		// Packfile section header
		fmt.Printf("[DEBUG upload-pack] writing packfile header, wants=%d, knownHaves=%d\n", len(fetchReq.Wants), len(knownHaves))
		if err := pktWriter.WriteLine("packfile"); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.Flush()
			return
		}

		// Stream packfile via native git pack-objects.
		// Merge client's existing shallow boundaries with newly computed grafts
		// so pack-objects limits traversal correctly.
		packShallow := append(fetchReq.Shallow, shallowGrafts...)
		flusher, hasFlusher := w.(http.Flusher)
		sbWriter := newSidebandWriter(w, 0x01)
		opts := &gitcmd.PackObjectsOpts{
			Wants:         fetchReq.Wants,
			Haves:         knownHaves,
			Shallow:       packShallow,
			ThinPack:      fetchReq.ThinPack,
			OfsDelta:      fetchReq.OfsDelta,
			IncludeTag:    fetchReq.IncludeTag,
			DeepenSince:   fetchReq.DeepenSince,
			DeepenExclude: fetchReq.DeepenNot,
			Filter:        fetchReq.Filter,
		}
		objCount, err := gitcmd.RunPackObjects(ctx, repoPath, opts, sbWriter)
		fmt.Printf("[DEBUG upload-pack] RunPackObjects: objects=%d, err=%v\n", objCount, err)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			if hasFlusher {
				flusher.Flush()
			}
			return
		}

		if err := sbWriter.Flush(); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
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
		_ = pktWriter.WriteLine("ERR unknown command")
		return
	}
}

func (h *UploadPackHandler) readSection(body io.Reader) ([]string, error) {
	return readPktLineSection(body)
}

// readPktLineSection reads one pkt-line section from the reader, collecting lines
// until a flush packet (0000) or delimiter packet (0001) is encountered.
// This is the canonical pkt-line section reader used by both HTTP and SSH handlers.
func readPktLineSection(body io.Reader) ([]string, error) {
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

		if length == 0 {
			break
		}
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

// handleLsRefsNative handles ls-refs using native git commands.
func handleLsRefsNative(ctx context.Context, repoPath string, lsRefsReq *LsRefsReq, pktWriter *pktLineWriter) error {
	// Resolve HEAD
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
		headInfo, err := gitcmd.ResolveHEAD(ctx, repoPath)
		if err == nil {
			if headInfo.IsSymbolic && lsRefsReq.Symrefs {
				if headInfo.OID != "" {
					line := fmt.Sprintf("%s HEAD symref-target:%s", headInfo.OID, headInfo.Target)
					if err := pktWriter.WriteLine(line); err != nil {
						return err
					}
				}
			} else if headInfo.OID != "" {
				if err := pktWriter.WriteLine(fmt.Sprintf("%s HEAD", headInfo.OID)); err != nil {
					return err
				}
			}
		}
	}

	// List refs using native git
	refs, err := gitcmd.ListRefs(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("failed to list references: %w", err)
	}

	for _, ref := range refs {
		refName := ref.RefName
		if refName == "HEAD" {
			continue
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
				continue
			}
		}

		// For tags with peel option
		attrs := make([]string, 0, 2)
		if lsRefsReq.Peel && strings.HasPrefix(refName, "refs/tags/") && ref.PeeledOID != "" {
			attrs = append(attrs, fmt.Sprintf("peeled:%s", ref.PeeledOID))
		}

		line := fmt.Sprintf("%s %s", ref.ObjectName, refName)
		if len(attrs) > 0 {
			line += " " + strings.Join(attrs, " ")
		}
		if err := pktWriter.WriteLine(line); err != nil {
			return err
		}
	}

	return nil
}

// sendEmptyPackfile sends a valid empty packfile (0 objects).
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

// Local methods removed: using performance.OptimizedPktLineWriter.WriteLine/Flush/WriteDelim directly

// RunPackObjectsForHTTP is a helper to run git pack-objects for HTTP upload-pack.
// It streams the packfile directly to the writer via sideband.
func RunPackObjectsForHTTP(ctx context.Context, repoPath string, fetchReq *FetchReq, w io.Writer) error {
	// Filter haves: only pass OIDs that exist on the server to pack-objects.
	// Clients may reference objects that were force-pushed away or GC'd.
	var knownHaves []string
	for _, have := range fetchReq.Haves {
		if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
			knownHaves = append(knownHaves, have)
		}
	}

	sbWriter := newSidebandWriter(w, 0x01)
	opts := &gitcmd.PackObjectsOpts{
		Wants:         fetchReq.Wants,
		Haves:         knownHaves,
		Shallow:       fetchReq.Shallow,
		ThinPack:      fetchReq.ThinPack,
		OfsDelta:      fetchReq.OfsDelta,
		IncludeTag:    fetchReq.IncludeTag,
		Deepen:        fetchReq.Deepen,
		DeepenSince:   fetchReq.DeepenSince,
		DeepenExclude: fetchReq.DeepenNot,
		Filter:        fetchReq.Filter,
	}
	_, err := gitcmd.RunPackObjects(ctx, repoPath, opts, sbWriter)
	if err != nil {
		return err
	}
	return sbWriter.Flush()
}

// RunGitPackObjectsRaw runs git pack-objects and writes raw pack data to writer (no sideband).
// Used by TCP server where sideband framing is handled externally.
func RunGitPackObjectsRaw(ctx context.Context, repoPath string, fetchReq *FetchReq, w io.Writer) error {
	if len(fetchReq.Wants) == 0 {
		return fmt.Errorf("no wants")
	}
	args := []string{"pack-objects", "--stdout", "--revs"}
	// NOTE: --depth in pack-objects is delta compression depth, NOT shallow clone depth.
	// Shallow clone depth is handled via --shallow <OID> lines in stdin.
	if fetchReq.DeepenSince > 0 {
		args = append(args, "--shallow-since="+strconv.FormatInt(fetchReq.DeepenSince, 10))
	}
	for _, ref := range fetchReq.DeepenNot {
		args = append(args, "--shallow-exclude="+ref)
	}
	if fetchReq.ThinPack {
		args = append(args, "--thin")
	}
	if fetchReq.OfsDelta {
		args = append(args, "--delta-base-offset")
	}
	if fetchReq.IncludeTag {
		args = append(args, "--include-tag")
	}
	if fetchReq.Filter != "" {
		args = append(args, "--filter="+fetchReq.Filter)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath

	var stdinBuf strings.Builder
	for _, s := range fetchReq.Shallow {
		stdinBuf.WriteString("--shallow ")
		stdinBuf.WriteString(s)
		stdinBuf.WriteByte('\n')
	}
	for _, want := range fetchReq.Wants {
		stdinBuf.WriteString(want)
		stdinBuf.WriteByte('\n')
	}
	for _, have := range fetchReq.Haves {
		// Only include haves that exist on the server — clients may reference
		// objects that were force-pushed away or GC'd.
		if _, err := gitcmd.ObjectExists(ctx, repoPath, have); err == nil {
			stdinBuf.WriteString("^")
			stdinBuf.WriteString(have)
			stdinBuf.WriteByte('\n')
		}
	}
	cmd.Stdin = strings.NewReader(stdinBuf.String())
	cmd.Stdout = w

	return cmd.Run()
}

// ReadPktLineSection exports readPktLineSection for use by other packages (e.g., TCP server).
func ReadPktLineSection(body io.Reader) ([]string, error) {
	return readPktLineSection(body)
}

// HandleLsRefsNative exports handleLsRefsNative for use by other packages (e.g., TCP server).
func HandleLsRefsNative(ctx context.Context, repoPath string, lsRefsReq *LsRefsReq, pktWriter *performance.OptimizedPktLineWriter) error {
	return handleLsRefsNative(ctx, repoPath, lsRefsReq, pktWriter)
}

// NewSidebandWriter exports sideband writer creation for use by other packages (e.g., TCP server).
func NewSidebandWriter(w io.Writer, channel byte) *performance.OptimizedSidebandWriter {
	return newSidebandWriter(w, channel)
}

// debug helpers
func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func containsStr(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

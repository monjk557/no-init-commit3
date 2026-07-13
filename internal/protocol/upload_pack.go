package protocol

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/collabops/git-executor/internal/packcache"
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

// Type aliases used by SSH/TCP protocol handlers.
type pktLineWriter = performance.OptimizedPktLineWriter
type sidebandWriter = performance.OptimizedSidebandWriter

func newSidebandWriter(w io.Writer, channel byte) *sidebandWriter {
	return performance.NewOptimizedSidebandWriter(w, channel)
}

type UploadPackHandler struct {
	pathConfig *repositories.PathConfig

	// idleTimeout sets the per-stream idle deadline applied to the
	// upload-pack response stream in lieu of http.Server.WriteTimeout
	// (see internal/protocol/streamdeadline.go for rationale).
	// Zero disables the deadline entirely.
	idleTimeout time.Duration

	// packEnv carries GIT_CONFIG_* entries that bound per-operation pack
	// memory (see pack_limits.go). Appended to the upload-pack command env so
	// the pack-objects child stays within a predictable RSS ceiling. Computed
	// once at construction; nil when all knobs are disabled.
	packEnv []string

	// cache deduplicates identical upload-pack responses (C3). Off by default;
	// nil or disabled means every request takes the direct-stream path below,
	// so behaviour is unchanged unless explicitly enabled. See internal/packcache.
	cache *packcache.Store
}

func NewUploadPackHandler(cfg *config.Config) (*UploadPackHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// A cache init failure must never break serving clones — fall back to the
	// direct path with a warning.
	cache, err := packcache.NewStore(packcache.FromEnv())
	if err != nil {
		fmt.Printf("[upload-pack] pack cache disabled (init failed: %v)\n", err)
		cache = nil
	}

	return &UploadPackHandler{
		pathConfig:  pathConfig,
		idleTimeout: cfg.Server.GitProtocolIdleTimeout,
		packEnv:     packLimitEnv(),
		cache:       cache,
	}, nil
}

// HandleInfoRefs handles Smart HTTP discovery for upload-pack (fetch) using native git.
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

	// Smart HTTP service header
	pktW := performance.NewOptimizedPktLineWriter(w)
	_ = pktW.WriteLine("# service=git-upload-pack")
	_ = pktW.Flush()

	// Delegate ref/capability advertisement to native git
	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", "--advertise-refs", repoPath)
	cmd.Stdout = w
	cmd.Env = gitProtocolEnv(r)

	if err := cmd.Run(); err != nil {
		fmt.Printf("[upload-pack] info/refs error: %v\n", err)
	}
}

// HandleUploadPack handles Git upload-pack POST requests using native git.
func (h *UploadPackHandler) HandleUploadPack(w http.ResponseWriter, r *http.Request) {
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

	// Limit request body to prevent OOM from oversized upload-pack negotiation.
	// Negotiation data (wants/haves) is typically < 1 MB.
	limitedBody := gitutil.LimitReadCloser(r.Body, gitutil.MaxRequestBodyBytes)
	bodyBytes, err := decompressRequestBody(limitedBody)
	if err != nil {
		http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
		return
	}

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	// Resolve the protocol version once for this POST. If the Git-Protocol
	// header was stripped in transit but the body is a v2 request, this
	// recovers version=2 so the response matches what info/refs advertised
	// (otherwise the client dies with `expected 'acknowledgments'`).
	proto := resolveGitProtocol(r, bodyBytes)
	if r.Header.Get("Git-Protocol") == "" && proto == gitProtocolV2 {
		fmt.Printf("[upload-pack] Git-Protocol header missing on POST; recovered version=2 from request body (proxy header desync)\n")
	}

	// Replace the http.Server's fixed WriteTimeout with a per-stream
	// idle deadline. pack-objects can stream for many minutes on cold
	// caches or large histories; an absolute deadline mid-pack
	// truncates the response and surfaces to the client as
	// `fatal: early EOF / fetch-pack: invalid index-pack output`.
	sd := NewStreamDeadliner(w, h.idleTimeout)
	if err := sd.Prime(); err != nil {
		fmt.Printf("[upload-pack] failed to install stream deadline: %v\n", err)
	}

	// C3 pack-objects cache: identical negotiations (e.g. a burst of parallel CI
	// clones of the same repo+SHA) coalesce onto one generation and reuse the
	// cached bytes. Returns true once the response is fully served; a false
	// return means nothing was written to w yet, so we fall through to the
	// direct path below (best-effort — never worse than no cache).
	if h.cache != nil && h.cache.Enabled() {
		if h.serveFromCache(r, owner, repo, repoPath, bodyBytes, proto, w, sd) {
			return
		}
	}

	// Flush the response header before pack generation starts. git upload-pack
	// emits no bytes until it finishes counting + delta-compressing objects,
	// which takes tens of seconds on a repo whose reachability bitmap is stale
	// or missing (Issue-5273). The fronting collabops-git-http reverse proxy
	// aborts the relay with HTTP 502 ("timeout awaiting response headers") when
	// headers do not arrive within its ResponseHeaderTimeout (~60s), so a slow
	// first byte fails an otherwise-healthy clone. Sending the header now
	// decouples header latency from pack-compute latency; the packfile body
	// then streams under the idle StreamDeadliner primed above.
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", repoPath)
	cmd.Stdin = bytes.NewReader(bodyBytes)
	mw := gitcmd.NewMeteredWriter(NewIdleWriter(w, sd))
	cmd.Stdout = mw
	// packEnv bounds pack-objects memory so concurrent clones cannot OOM the
	// pod (Issue-5071). Applied here (the pack-generating call), not on the
	// advertise-refs path which does no pack generation.
	cmd.Env = append(protoEnv(proto), h.packEnv...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Network-independent server-processing metric. CPUTime aggregates the
	// upload-pack process and its pack-objects child (wait4 rusage), so it
	// reflects pure server compute regardless of how slowly the client drains.
	start := time.Now()
	runErr := cmd.Run()
	gitcmd.RecordTransfer(gitcmd.TransferStats{
		Operation: "upload-pack",
		Transport: "http",
		CPUTime:   gitcmd.CPUTime(cmd.ProcessState),
		WallTime:  time.Since(start),
		TTFB:      gitcmd.TTFB(start, mw.FirstByteAt()),
		Bytes:     mw.Bytes(),
		Success:   runErr == nil,
	})
	if runErr != nil {
		fmt.Printf("[upload-pack] error: %v, stderr: %s\n", runErr, stderr.String())
	}
}

// serveFromCache serves the upload-pack response through the C3 pack cache.
//
// Cache key = (owner/repo, request body). This is correct without a separate
// generation token: `git upload-pack --stateless-rpc` output is deterministic
// for a given negotiation (wants/haves/capabilities), and the wanted objects
// are immutable content-addressed commits — a clone of a newly-pushed SHA sends
// a different body and thus misses. The TTL bounds retention/eviction, not
// correctness.
//
// On a miss the pack is generated to a cache file (single-flight coalesces
// concurrent identical misses onto one generation) and then streamed to the
// client. Trade-off: the miss path buffers to the local cache file before the
// first byte reaches the client, rather than tee-streaming live; acceptable for
// the small shallow packs CI produces. A streamcache-style live tee is a
// documented follow-up.
//
// Returns true iff it fully handled the response. A false return guarantees
// nothing was written to w (the generation writes to the cache file first), so
// the caller may safely fall back to the direct path.
func (h *UploadPackHandler) serveFromCache(
	r *http.Request,
	owner, repo, repoPath string,
	bodyBytes []byte,
	proto string,
	w http.ResponseWriter,
	sd *StreamDeadliner,
) bool {
	key := packcache.DeriveKey(owner+"/"+repo, "", bodyBytes)

	f, hit, err := h.cache.GetOrGenerate(key, func(cw io.Writer) error {
		cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", repoPath)
		cmd.Stdin = bytes.NewReader(bodyBytes)
		cmd.Stdout = cw
		cmd.Env = append(protoEnv(proto), h.packEnv...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if runErr := cmd.Run(); runErr != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), runErr)
		}
		return nil
	})
	if err != nil {
		// Nothing was streamed to the client yet — fall back to direct serving.
		fmt.Printf("[upload-pack] cache path failed (%v), falling back to direct\n", err)
		return false
	}
	defer func() { _ = f.Close() }()

	mw := gitcmd.NewMeteredWriter(NewIdleWriter(w, sd))
	start := time.Now()
	_, copyErr := io.Copy(mw, f)
	gitcmd.RecordTransfer(gitcmd.TransferStats{
		Operation: "upload-pack",
		Transport: "http-cache",
		WallTime:  time.Since(start),
		TTFB:      gitcmd.TTFB(start, mw.FirstByteAt()),
		Bytes:     mw.Bytes(),
		Success:   copyErr == nil,
	})
	if copyErr != nil {
		fmt.Printf("[upload-pack] cache stream error (hit=%v): %v\n", hit, copyErr)
	}
	return true
}

// decompressRequestBody reads the request body and decompresses gzip if detected.
func decompressRequestBody(body io.ReadCloser) ([]byte, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		data, err = io.ReadAll(gr)
		gr.Close()
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
	}

	return data, nil
}

// gitProtocolV2 is the GIT_PROTOCOL value that makes git speak protocol v2.
const gitProtocolV2 = "version=2"

// gitProtocolEnv returns the os environment with GIT_PROTOCOL set from the
// request header. Used on the info/refs GET path, which carries no body to
// derive the version from.
func gitProtocolEnv(r *http.Request) []string {
	return protoEnv(r.Header.Get("Git-Protocol"))
}

// gitProtocolEnvForBody returns the os environment with GIT_PROTOCOL resolved
// for a git-upload-pack POST. It prefers the Git-Protocol request header but
// falls back to sniffing the (decompressed) request body when the header is
// absent — see resolveGitProtocol.
func gitProtocolEnvForBody(r *http.Request, body []byte) []string {
	return protoEnv(resolveGitProtocol(r, body))
}

func protoEnv(proto string) []string {
	env := os.Environ()
	if proto != "" {
		env = append(env, "GIT_PROTOCOL="+proto)
	}
	return env
}

// resolveGitProtocol determines the protocol version git upload-pack should
// speak for a fetch POST.
//
// Smart HTTP fetch is a two-phase exchange: the client first GETs
// /info/refs?service=git-upload-pack (capability advertisement) and then POSTs
// to /git-upload-pack (negotiation + packfile). The version is carried on the
// non-standard Git-Protocol request header on BOTH requests. A fronting
// reverse proxy / load balancer can intermittently drop that header on the
// POST while it survived on the GET; git upload-pack then falls back to
// protocol v0 and emits a v0 response, but the client — which saw a v2
// advertisement at info/refs — parses the POST response with the v2 state
// machine and dies with `fatal: expected 'acknowledgments'` (the
// acknowledgments section exists only in v2).
//
// The request body is the authoritative record of what the client is actually
// speaking: a v2 fetch request begins with a `command=fetch` (or
// `command=ls-refs`) pkt-line, which a v0 request never contains. So when the
// header is missing we recover the version from the body, guaranteeing the
// POST response version always matches what info/refs advertised regardless of
// header propagation. The header remains authoritative when present, so
// correctly-proxied requests are unaffected.
func resolveGitProtocol(r *http.Request, body []byte) string {
	if proto := r.Header.Get("Git-Protocol"); proto != "" {
		return proto
	}
	if isProtocolV2Body(body) {
		return gitProtocolV2
	}
	return ""
}

// isProtocolV2Body reports whether a git-upload-pack request body is a
// protocol v2 command request, detected by a leading `command=` pkt-line.
func isProtocolV2Body(body []byte) bool {
	line, ok := firstPktLineContent(body)
	if !ok {
		return false
	}
	return bytes.HasPrefix(line, []byte("command="))
}

// firstPktLineContent returns the payload of the first pkt-line in body, with
// the trailing LF trimmed. It returns ok=false for empty input, a malformed
// length prefix, or special pkt-lines (flush/delim/response-end, length < 4).
func firstPktLineContent(body []byte) ([]byte, bool) {
	if len(body) < 4 {
		return nil, false
	}
	n, err := strconv.ParseUint(string(body[:4]), 16, 16)
	if err != nil {
		return nil, false
	}
	// 0000 flush, 0001 delim, 0002 response-end carry no payload.
	if n < 4 || int(n) > len(body) {
		return nil, false
	}
	return bytes.TrimRight(body[4:n], "\n"), true
}

// ---------------------------------------------------------------------------
// SSH/TCP compatibility functions below.
// These are used by SSH and TCP protocol handlers which still do manual
// pkt-line handling. The HTTP handlers above use native git processes instead.
// ---------------------------------------------------------------------------

func (h *UploadPackHandler) readSection(body io.Reader) ([]string, error) {
	return readPktLineSection(body)
}

// readPktLineSection reads one pkt-line section from the reader, collecting lines
// until a flush packet (0000) or delimiter packet (0001) is encountered.
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

// RunPackObjectsForHTTP streams packfile via sideband for SSH/TCP handlers.
func RunPackObjectsForHTTP(ctx context.Context, repoPath string, fetchReq *FetchReq, w io.Writer) error {
	existingHaves, err := gitcmd.FilterExistingObjects(ctx, repoPath, fetchReq.Haves)
	if err != nil {
		return fmt.Errorf("filter haves: %w", err)
	}
	var knownHaves []string
	for _, have := range fetchReq.Haves {
		if existingHaves[have] {
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
	_, err = gitcmd.RunPackObjects(ctx, repoPath, opts, sbWriter)
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
	existingHaves, err := gitcmd.FilterExistingObjects(ctx, repoPath, fetchReq.Haves)
	if err != nil {
		return fmt.Errorf("filter haves: %w", err)
	}
	for _, have := range fetchReq.Haves {
		if existingHaves[have] {
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

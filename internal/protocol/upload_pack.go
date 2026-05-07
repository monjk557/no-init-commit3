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
}

func NewUploadPackHandler(cfg *config.Config) (*UploadPackHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &UploadPackHandler{
		pathConfig:  pathConfig,
		idleTimeout: cfg.Server.GitProtocolIdleTimeout,
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

	// Replace the http.Server's fixed WriteTimeout with a per-stream
	// idle deadline. pack-objects can stream for many minutes on cold
	// caches or large histories; an absolute deadline mid-pack
	// truncates the response and surfaces to the client as
	// `fatal: early EOF / fetch-pack: invalid index-pack output`.
	sd := NewStreamDeadliner(w, h.idleTimeout)
	if err := sd.Prime(); err != nil {
		fmt.Printf("[upload-pack] failed to install stream deadline: %v\n", err)
	}

	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", repoPath)
	cmd.Stdin = bytes.NewReader(bodyBytes)
	cmd.Stdout = NewIdleWriter(w, sd)
	cmd.Env = gitProtocolEnv(r)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		fmt.Printf("[upload-pack] error: %v, stderr: %s\n", err, stderr.String())
	}
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

// gitProtocolEnv returns os environment with GIT_PROTOCOL set from the request header.
func gitProtocolEnv(r *http.Request) []string {
	env := os.Environ()
	if proto := r.Header.Get("Git-Protocol"); proto != "" {
		env = append(env, "GIT_PROTOCOL="+proto)
	}
	return env
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

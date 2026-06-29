package gitcmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PackObjectsOpts holds options for git pack-objects.
type PackObjectsOpts struct {
	Wants   []string // OIDs the client wants
	Haves   []string // OIDs the client already has
	Shallow []string // client shallow boundaries

	ThinPack        bool
	OfsDelta        bool
	IncludeTag      bool
	DeltaBaseOffset bool

	// Shallow/deepen options (native git handles these)
	Deepen        int
	DeepenSince   int64    // unix timestamp
	DeepenExclude []string // refs to exclude from shallow

	// Partial clone filter (e.g. "blob:none", "tree:0")
	Filter string

	// Transport labels the emitted transfer metric ("ssh" | "tcp"). HTTP
	// download uses native git upload-pack, not this path. Empty -> "unknown".
	Transport string

	// Progress callback: called with stderr progress lines
	OnProgress func(line string)
}

// RunPackObjects runs `git pack-objects --stdout --revs` and streams the packfile
// to the given writer. Returns the number of objects packed. It records a
// network-independent transfer metric (CPU time, bytes, TTFB, objects, reused)
// via the installed TransferMetricsRecorder, if any.
func RunPackObjects(ctx context.Context, repoPath string, opts *PackObjectsOpts, w io.Writer) (objectCount int, err error) {
	args := []string{"pack-objects", "--stdout", "--revs"}

	if opts.ThinPack {
		args = append(args, "--thin")
	}
	if opts.OfsDelta {
		args = append(args, "--delta-base-offset")
	}
	if opts.IncludeTag {
		args = append(args, "--include-tag")
	}

	// Shallow/deepen options
	// NOTE: --depth in pack-objects controls delta compression chain depth,
	// NOT shallow clone depth. Shallow clone depth is handled by passing
	// --shallow <OID> lines in stdin (see below). Do NOT pass opts.Deepen here.
	if opts.DeepenSince > 0 {
		args = append(args, "--shallow-since="+strconv.FormatInt(opts.DeepenSince, 10))
	}
	for _, ref := range opts.DeepenExclude {
		args = append(args, "--shallow-exclude="+ref)
	}

	// Partial clone filter
	if opts.Filter != "" {
		args = append(args, "--filter="+opts.Filter)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath

	// Build stdin: wants and haves in rev-list format
	var stdinBuf strings.Builder
	// Client shallow state: --shallow <OID> lines
	for _, s := range opts.Shallow {
		stdinBuf.WriteString("--shallow ")
		stdinBuf.WriteString(s)
		stdinBuf.WriteByte('\n')
	}
	for _, want := range opts.Wants {
		stdinBuf.WriteString(want)
		stdinBuf.WriteByte('\n')
	}
	for _, have := range opts.Haves {
		stdinBuf.WriteString("^")
		stdinBuf.WriteString(have)
		stdinBuf.WriteByte('\n')
	}
	cmd.Stdin = strings.NewReader(stdinBuf.String())
	mw := NewMeteredWriter(w)
	cmd.Stdout = mw

	// Capture stderr for progress parsing
	stderrPipe, perr := cmd.StderrPipe()
	if perr != nil {
		return 0, fmt.Errorf("stderr pipe: %w", perr)
	}

	start := time.Now()
	reused := 0
	// Emit a network-independent transfer metric regardless of success path.
	// objectCount/reused/err are named returns captured at defer time.
	defer func() {
		RecordTransfer(TransferStats{
			Operation: "upload-pack",
			Transport: opts.Transport,
			CPUTime:   CPUTime(cmd.ProcessState),
			WallTime:  time.Since(start),
			TTFB:      TTFB(start, mw.FirstByteAt()),
			Bytes:     mw.Bytes(),
			Objects:   objectCount,
			Reused:    reused,
			Success:   err == nil,
		})
	}()

	if err = cmd.Start(); err != nil {
		return 0, fmt.Errorf("start git pack-objects: %w", err)
	}

	// Parse stderr for progress and object count; capture fatal/error lines for diagnostics
	var stderrErrors []string
	scanner := bufio.NewScanner(stderrPipe)
	scanner.Split(scanCROrLF)
	for scanner.Scan() {
		line := scanner.Text()
		if opts.OnProgress != nil {
			opts.OnProgress(line)
		}
		// Parse "Total N (delta D), reused R (delta D)" for object/reuse counts
		if n := parsePackObjectsTotal(line); n > 0 {
			objectCount = n
		}
		if r := parsePackObjectsReused(line); r >= 0 {
			reused = r
		}
		// Capture fatal/error lines for error reporting
		if strings.HasPrefix(line, "fatal:") || strings.HasPrefix(line, "error:") {
			stderrErrors = append(stderrErrors, line)
		}
	}

	if werr := cmd.Wait(); werr != nil {
		if len(stderrErrors) > 0 {
			err = fmt.Errorf("git pack-objects: %w (%s)", werr, strings.Join(stderrErrors, "; "))
		} else {
			err = fmt.Errorf("git pack-objects: %w", werr)
		}
		return objectCount, err
	}

	return objectCount, nil
}

// parsePackObjectsTotal extracts the total object count from pack-objects stderr.
// Line format: "Total N (delta D), reused R (delta D), pack-reused P"
func parsePackObjectsTotal(line string) int {
	if !strings.HasPrefix(line, "Total ") {
		return 0
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return n
}

// parsePackObjectsReused extracts the reused object count from pack-objects
// stderr ("Total N (delta D), reused R (delta D2)"). Returns -1 when the line is
// not a Total line or the count cannot be parsed, so callers can ignore it.
func parsePackObjectsReused(line string) int {
	if !strings.HasPrefix(line, "Total ") {
		return -1
	}
	const marker = "reused "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return -1
	}
	fields := strings.Fields(line[idx+len(marker):])
	if len(fields) == 0 {
		return -1
	}
	r, err := strconv.Atoi(fields[0])
	if err != nil {
		return -1
	}
	return r
}

// scanCROrLF is a bufio.SplitFunc that splits on \n or \r (for progress lines).
func scanCROrLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

package gitcmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
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

	// Progress callback: called with stderr progress lines
	OnProgress func(line string)
}

// RunPackObjects runs `git pack-objects --stdout --revs` and streams the packfile
// to the given writer. Returns the number of objects packed.
func RunPackObjects(ctx context.Context, repoPath string, opts *PackObjectsOpts, w io.Writer) (int, error) {
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
	cmd.Stdout = w

	// Capture stderr for progress parsing
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start git pack-objects: %w", err)
	}

	// Parse stderr for progress and object count
	objectCount := 0
	scanner := bufio.NewScanner(stderrPipe)
	scanner.Split(scanCROrLF)
	for scanner.Scan() {
		line := scanner.Text()
		if opts.OnProgress != nil {
			opts.OnProgress(line)
		}
		// Parse "Total N (delta D), reused R (delta D)" for object count
		if n := parsePackObjectsTotal(line); n > 0 {
			objectCount = n
		}
	}

	if err := cmd.Wait(); err != nil {
		return objectCount, fmt.Errorf("git pack-objects: %w", err)
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

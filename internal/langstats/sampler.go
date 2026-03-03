package langstats

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-enry/go-enry/v2"
)

const sampleReadLimit = 8 * 1024 // 8KB per sample

// SampleAndResolve는 모호한 확장자 그룹을 샘플링하여 언어를 결정합니다 (Phase 2).
// 단일 git cat-file --batch 프로세스로 모든 샘플을 처리합니다.
// 결과는 langStats에 직접 반영됩니다.
func SampleAndResolve(ctx context.Context, repoPath string, groups map[string]*extensionGroup, langStats map[string]*langAccum) (int, error) {
	if len(groups) == 0 {
		return 0, nil
	}

	// Collect all sample hashes across groups
	type sampleRef struct {
		ext  string
		path string
	}
	var refs []sampleRef
	for ext, group := range groups {
		for _, s := range group.samples {
			refs = append(refs, sampleRef{ext: ext, path: s.path})
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}

	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("git cat-file start: %w", err)
	}

	reader := bufio.NewReaderSize(stdout, sampleReadLimit+1024)
	filesSampled := 0

	// Per-extension vote tallies
	type voteEntry struct {
		lang  string
		count int
	}
	votes := make(map[string][]voteEntry) // ext → votes

	for _, ref := range refs {
		group := groups[ref.ext]
		sample := group.samples[filesSampled%len(group.samples)]
		// Find matching sample by path
		for _, s := range group.samples {
			if s.path == ref.path {
				sample = s
				break
			}
		}

		// Send hash to stdin
		_, err := fmt.Fprintf(stdin, "%s\n", sample.hash)
		if err != nil {
			break
		}

		// Read response header: "<hash> <type> <size>\n"
		headerLine, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		headerLine = strings.TrimRight(headerLine, "\n")

		// Check for "missing" object
		if strings.HasSuffix(headerLine, " missing") {
			continue
		}

		parts := strings.Fields(headerLine)
		if len(parts) < 3 {
			continue
		}

		objSize, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			continue
		}

		// Read content (up to sampleReadLimit)
		readSize := objSize
		if readSize > sampleReadLimit {
			readSize = sampleReadLimit
		}
		content := make([]byte, readSize)
		_, err = io.ReadFull(reader, content)
		if err != nil {
			break
		}

		// Discard remaining content + trailing newline
		remaining := objSize - readSize + 1 // +1 for trailing \n
		if remaining > 0 {
			_, _ = io.CopyN(io.Discard, reader, remaining)
		}

		filesSampled++

		// Skip binary files
		if enry.IsBinary(content) {
			continue
		}

		// Detect language
		lang := enry.GetLanguage(filepath.Base(sample.path), content)
		if lang == "" {
			continue
		}

		// Record vote
		extVotes := votes[ref.ext]
		found := false
		for i := range extVotes {
			if extVotes[i].lang == lang {
				extVotes[i].count++
				found = true
				break
			}
		}
		if !found {
			votes[ref.ext] = append(extVotes, voteEntry{lang: lang, count: 1})
		}
	}

	_ = stdin.Close()
	_ = cmd.Wait()

	// Proportionally distribute bytes/files for each extension group
	for ext, group := range groups {
		extVotes, ok := votes[ext]
		if !ok || len(extVotes) == 0 {
			// No valid samples → skip this group entirely
			continue
		}

		totalVotes := 0
		for _, v := range extVotes {
			totalVotes += v.count
		}

		for _, v := range extVotes {
			ratio := float64(v.count) / float64(totalVotes)
			distribBytes := int64(math.Round(float64(group.totalBytes) * ratio))
			distribFiles := int(math.Round(float64(group.totalFiles) * ratio))

			acc := langStats[v.lang]
			if acc == nil {
				acc = &langAccum{}
				langStats[v.lang] = acc
			}
			acc.bytes += distribBytes
			acc.files += distribFiles
		}
	}

	return filesSampled, nil
}

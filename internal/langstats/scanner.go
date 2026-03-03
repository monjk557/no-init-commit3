package langstats

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-enry/go-enry/v2"
)

const maxSamplesPerExt = 10

// StreamTreeAndClassify는 git ls-tree를 스트리밍하며 파일을 분류합니다 (Phase 1).
// 확장자만으로 언어가 확정되는 파일은 즉시 집계하고,
// 모호한 확장자는 extensionGroup으로 모아 Phase 2 샘플링에 넘깁니다.
func StreamTreeAndClassify(ctx context.Context, repoPath, ref string) (*scanResult, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "ls-tree", "-r", "--long", ref)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git ls-tree start: %w", err)
	}

	result := &scanResult{
		langStats:       make(map[string]*langAccum),
		ambiguousGroups: make(map[string]*extensionGroup),
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()

		mode, entryType, hash, size, path, ok := parseLsTreeLong(line)
		if !ok {
			continue
		}

		// Skip non-blobs
		if entryType != "blob" {
			continue
		}

		// Skip symlinks (mode 120000)
		if mode == "120000" {
			result.totalSkipped++
			continue
		}

		// Skip empty files and files > 1MB
		if size == 0 || size > 1024*1024 {
			result.totalSkipped++
			continue
		}

		result.totalScanned++

		filename := filepath.Base(path)

		// Apply linguist-style filters
		if enry.IsVendor(path) || enry.IsDotFile(filename) || enry.IsDocumentation(path) || enry.IsConfiguration(path) {
			result.totalSkipped++
			continue
		}

		// Try extension-based detection
		lang, safe := enry.GetLanguageByExtension(filename)
		if safe && lang != "" {
			// Fast path: extension uniquely identifies the language
			acc := result.langStats[lang]
			if acc == nil {
				acc = &langAccum{}
				result.langStats[lang] = acc
			}
			acc.bytes += size
			acc.files++
			continue
		}

		// Ambiguous or unknown extension → defer to sampling
		ext := filepath.Ext(filename)
		if ext == "" {
			// No extension → skip (filename-based detection is unreliable without content)
			result.totalSkipped++
			continue
		}

		group := result.ambiguousGroups[ext]
		if group == nil {
			// Determine candidate languages for this extension
			var candidates []string
			if lang != "" {
				candidates = []string{lang}
			}
			// Also check GetLanguagesByExtension for multiple candidates
			multiLangs := enry.GetLanguagesByExtension(filename, nil, nil)
			if len(multiLangs) > len(candidates) {
				candidates = multiLangs
			}
			group = &extensionGroup{
				candidates: candidates,
			}
			result.ambiguousGroups[ext] = group
		}
		group.totalBytes += size
		group.totalFiles++
		if len(group.samples) < maxSamplesPerExt {
			group.samples = append(group.samples, sampleCandidate{
				hash: hash,
				path: path,
				size: size,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("git ls-tree failed: %w", err)
	}

	return result, nil
}

// parseLsTreeLong은 `git ls-tree -r --long` 한 줄을 파싱합니다.
// 형식: <mode> <type> <hash> <size>\t<path>
func parseLsTreeLong(line string) (mode, entryType, hash string, size int64, path string, ok bool) {
	tabIdx := strings.IndexByte(line, '\t')
	if tabIdx < 0 {
		return
	}
	path = line[tabIdx+1:]
	meta := strings.Fields(line[:tabIdx])
	if len(meta) < 4 {
		return
	}
	mode = meta[0]
	entryType = meta[1]
	hash = meta[2]

	if meta[3] != "-" {
		n, err := strconv.ParseInt(meta[3], 10, 64)
		if err != nil {
			return
		}
		size = n
	}
	ok = true
	return
}

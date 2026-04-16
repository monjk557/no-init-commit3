package langstats

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/go-enry/go-enry/v2/data"
)

const (
	minLanguagePercentage = 1.0
	maxLanguagesToShow    = 5
)

// ComputeLanguageStats는 레포지토리의 언어 분포를 계산합니다.
// Phase 1(스트리밍 스캔) → Phase 2(샘플링) → Phase 3(정리) 파이프라인을 실행합니다.
func ComputeLanguageStats(ctx context.Context, repoPath, ref string) (*LanguageStatsResponse, error) {
	start := time.Now()

	// 1. ref resolve
	commitSHA, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve ref: %w", err)
	}

	// 2. Phase 1: Streaming scan
	scan, err := StreamTreeAndClassify(ctx, repoPath, commitSHA)
	if err != nil {
		return nil, fmt.Errorf("phase 1 scan: %w", err)
	}

	// 3. Phase 2: Sample ambiguous groups
	filesSampled, err := SampleAndResolve(ctx, repoPath, scan.ambiguousGroups, scan.langStats)
	if err != nil {
		return nil, fmt.Errorf("phase 2 sample: %w", err)
	}

	// 4. Phase 3: Finalize
	languages, totalBytes, totalFiles := finalize(scan.langStats)

	filesDetectedByExt := scan.totalScanned - scan.totalSkipped
	for _, g := range scan.ambiguousGroups {
		filesDetectedByExt -= g.totalFiles
	}

	elapsed := time.Since(start)

	return &LanguageStatsResponse{
		Success:    true,
		Repository: "",
		CommitSHA:  commitSHA,
		Languages:  languages,
		TotalBytes: totalBytes,
		TotalFiles: totalFiles,
		Meta: ComputationMeta{
			TotalFilesScanned:  scan.totalScanned,
			FilesSkipped:       scan.totalSkipped,
			FilesDetectedByExt: filesDetectedByExt,
			FilesSampled:       filesSampled,
			ElapsedMs:          int(elapsed.Milliseconds()),
		},
	}, nil
}

// resolveRef는 git rev-parse로 ref를 커밋 SHA로 변환합니다.
func resolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	target := ref
	if target == "" {
		target = "HEAD"
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", target)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// finalize는 langStats를 최종 응답 형식으로 변환합니다.
// LanguagesGroup 매핑, 정렬, top 5 + Others 처리를 수행합니다.
func finalize(langStats map[string]*langAccum) ([]LanguageStat, int64, int) {
	// LanguagesGroup 매핑 (TSX→TypeScript, JSX→JavaScript 등)
	merged := make(map[string]*langAccum, len(langStats))
	for lang, acc := range langStats {
		resolved := lang
		if base, exists := data.LanguagesGroup[lang]; exists {
			resolved = base
		}
		m := merged[resolved]
		if m == nil {
			m = &langAccum{}
			merged[resolved] = m
		}
		m.bytes += acc.bytes
		m.files += acc.files
	}

	// 총합 계산
	var totalBytes int64
	var totalFiles int
	for _, acc := range merged {
		totalBytes += acc.bytes
		totalFiles += acc.files
	}

	if totalBytes == 0 {
		return []LanguageStat{}, 0, 0
	}

	// LanguageStat 슬라이스 변환 + 퍼센티지 계산
	stats := make([]LanguageStat, 0, len(merged))
	for lang, acc := range merged {
		stats = append(stats, LanguageStat{
			Language:   lang,
			Bytes:      acc.bytes,
			Percentage: (float64(acc.bytes) / float64(totalBytes)) * 100,
			Files:      acc.files,
		})
	}

	// bytes 기준 내림차순 정렬
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Bytes > stats[j].Bytes
	})

	// Top 5 + Others (1% 미만 합산)
	filtered := make([]LanguageStat, 0, maxLanguagesToShow+1)
	var othersBytes int64
	var othersFiles int
	sumVisiblePct := 0.0

	for _, s := range stats {
		if s.Percentage >= minLanguagePercentage && len(filtered) < maxLanguagesToShow {
			filtered = append(filtered, s)
			sumVisiblePct += s.Percentage
			continue
		}
		othersBytes += s.Bytes
		othersFiles += s.Files
	}

	if othersBytes > 0 {
		othersPct := 100.0 - sumVisiblePct
		if othersPct < 0 {
			othersPct = 0
		} else if othersPct > 100 {
			othersPct = 100
		}
		filtered = append(filtered, LanguageStat{
			Language:   "Others",
			Bytes:      othersBytes,
			Percentage: othersPct,
			Files:      othersFiles,
		})
	}

	return filtered, totalBytes, totalFiles
}

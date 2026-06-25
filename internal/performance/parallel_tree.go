package performance

import (
	"context"
	"io"
	"runtime"
	"sync"

	"github.com/go-enry/go-enry/v2"
	"github.com/go-enry/go-enry/v2/data"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// ParallelTreeProcessor provides high-performance concurrent tree traversal
// Research target: Reduce sequential tree.Files().ForEach() bottleneck by 50-60%
type ParallelTreeProcessor struct {
	workerCount int
	logger      *zap.Logger
}

// FileProcessResult contains the result of processing a single file
type FileProcessResult struct {
	Language   string
	Bytes      int64
	Files      int
	Error      error
	Skipped    bool
	SkipReason string
}

// LanguageStats represents aggregated language statistics
type LanguageStats struct {
	Language   string  `json:"language"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
	Files      int     `json:"files"`
}

// NewParallelTreeProcessor creates a new parallel tree processor
func NewParallelTreeProcessor(logger *zap.Logger) *ParallelTreeProcessor {
	// Use CPU count for optimal parallelism, but cap it for memory usage
	workerCount := runtime.NumCPU()
	if workerCount > 8 {
		workerCount = 8 // Prevent excessive memory usage
	}
	if workerCount < 2 {
		workerCount = 2 // Minimum parallelism
	}

	return &ParallelTreeProcessor{
		workerCount: workerCount,
		logger:      logger,
	}
}

// ProcessTreeForLanguages processes tree files in parallel for language detection
func (ptp *ParallelTreeProcessor) ProcessTreeForLanguages(ctx context.Context, tree *object.Tree) (map[string]LanguageStats, int64, int, error) {
	// Create channels for work distribution
	fileChan := make(chan *object.File, 100)        // Buffer files for workers
	resultChan := make(chan FileProcessResult, 100) // Buffer results

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < ptp.workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			ptp.fileWorker(ctx, fileChan, resultChan, workerID)
		}(i)
	}

	// Start result collector goroutine
	langStats := make(map[string]struct {
		bytes int64
		files int
	})
	var totalBytes int64
	var totalFiles int
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)

	go func() {
		defer collectorWG.Done()
		for result := range resultChan {
			if result.Error != nil {
				ptp.logger.Debug("File processing error", zap.Error(result.Error))
				continue
			}
			if result.Skipped {
				ptp.logger.Debug("File skipped", zap.String("reason", result.SkipReason))
				continue
			}

			// Aggregate statistics
			stats := langStats[result.Language]
			stats.bytes += result.Bytes
			stats.files += result.Files
			langStats[result.Language] = stats

			totalBytes += result.Bytes
			totalFiles += result.Files
		}
	}()

	// Send files to workers
	go func() {
		defer close(fileChan)

		err := tree.Files().ForEach(func(file *object.File) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case fileChan <- file:
				return nil
			}
		})

		if err != nil && err != io.EOF {
			ptp.logger.Warn("Error iterating files", zap.Error(err))
		}
	}()

	// Wait for all workers to finish, then close result channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Wait for result collector to finish
	collectorWG.Wait()

	// Convert to final format
	finalStats := make(map[string]LanguageStats)
	for lang, stats := range langStats {
		percentage := float64(0)
		if totalBytes > 0 {
			percentage = (float64(stats.bytes) / float64(totalBytes)) * 100
		}

		finalStats[lang] = LanguageStats{
			Language:   lang,
			Bytes:      stats.bytes,
			Percentage: percentage,
			Files:      stats.files,
		}
	}

	ptp.logger.Debug("Parallel tree processing completed",
		zap.Int("total_files", totalFiles),
		zap.Int64("total_bytes", totalBytes),
		zap.Int("languages", len(finalStats)),
		zap.Int("worker_count", ptp.workerCount))

	return finalStats, totalBytes, totalFiles, nil
}

// fileWorker processes files from the channel
func (ptp *ParallelTreeProcessor) fileWorker(ctx context.Context, fileChan <-chan *object.File, resultChan chan<- FileProcessResult, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case file, ok := <-fileChan:
			if !ok {
				return // Channel closed
			}

			result := ptp.processFile(ctx, file)

			select {
			case <-ctx.Done():
				return
			case resultChan <- result:
			}
		}
	}
}

// processFile processes a single file for language detection
func (ptp *ParallelTreeProcessor) processFile(ctx context.Context, file *object.File) FileProcessResult {
	// Context cancellation check
	select {
	case <-ctx.Done():
		return FileProcessResult{Error: ctx.Err()}
	default:
	}

	// Apply filtering rules (same as original logic)
	if enry.IsVendor(file.Name) {
		return FileProcessResult{Skipped: true, SkipReason: "vendor"}
	}
	if enry.IsDotFile(file.Name) {
		return FileProcessResult{Skipped: true, SkipReason: "dotfile"}
	}
	if enry.IsDocumentation(file.Name) {
		return FileProcessResult{Skipped: true, SkipReason: "documentation"}
	}
	if enry.IsConfiguration(file.Name) {
		return FileProcessResult{Skipped: true, SkipReason: "configuration"}
	}

	// Skip very large files (>1MB), consistent with the smart/sequential paths.
	if file.Size > LanguageStatsMaxFileSize {
		return FileProcessResult{Skipped: true, SkipReason: "too_large"}
	}

	// Read file content for language detection
	reader, err := file.Reader()
	if err != nil {
		return FileProcessResult{Skipped: true, SkipReason: "read_error"}
	}

	content, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return FileProcessResult{Skipped: true, SkipReason: "read_error"}
	}

	// Skip binary files
	if enry.IsBinary(content) {
		return FileProcessResult{Skipped: true, SkipReason: "binary"}
	}

	// Skip generated files
	if enry.IsGenerated(file.Name, content) {
		return FileProcessResult{Skipped: true, SkipReason: "generated"}
	}

	// Detect language
	language := enry.GetLanguage(file.Name, content)
	if language == "" {
		language, _ = enry.GetLanguageByFilename(file.Name)
	}
	if language == "" {
		return FileProcessResult{Skipped: true, SkipReason: "unknown_language"}
	}

	// Map derived languages to base languages
	if baseLanguage, exists := data.LanguagesGroup[language]; exists {
		language = baseLanguage
	}

	return FileProcessResult{
		Language: language,
		Bytes:    file.Size,
		Files:    1,
		Error:    nil,
		Skipped:  false,
	}
}

// GetOptimalWorkerCount returns the optimal number of workers for tree processing
func (ptp *ParallelTreeProcessor) GetOptimalWorkerCount() int {
	return ptp.workerCount
}

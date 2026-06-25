package performance

import (
	"context"
	"io"
	"sync"

	"github.com/go-enry/go-enry/v2"
	"github.com/go-enry/go-enry/v2/data"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// LanguageStatsMaxFileSize is the upper bound for files considered in language
// distribution. Files larger than this are skipped, matching GitHub/GitLab
// (linguist) behavior. All three detection paths (smart, parallel, sequential)
// must use the same limit so results are consistent regardless of which path runs.
const LanguageStatsMaxFileSize int64 = 1024 * 1024 // 1MB

// SmartLanguageDetector provides optimized language detection with sampling
// Research target: Reduce 50-80% overhead from reading entire files
type SmartLanguageDetector struct {
	logger      *zap.Logger
	workerCount int

	// Sampling configuration
	sampleSize        int   // How many bytes to read for detection (default: 8KB)
	minConfidenceSize int   // Minimum bytes needed for high confidence (default: 1KB)
	maxFileSize       int64 // Skip files larger than this (default: 10MB)
}

// SmartLanguageResult represents the result of smart language detection
type SmartLanguageResult struct {
	Language   string
	Bytes      int64
	Files      int
	Confidence float64 // Detection confidence (0.0-1.0)
	Method     DetectionMethod
	Error      error
	Skipped    bool
	SkipReason string
}

// DetectionMethod indicates how the language was detected
type DetectionMethod int

const (
	DetectionByExtension DetectionMethod = iota // Detected by file extension only
	DetectionBySampling                         // Detected by content sampling
	DetectionByFull                             // Detected by full content (fallback)
)

// NewSmartLanguageDetector creates a new optimized language detector
func NewSmartLanguageDetector(logger *zap.Logger) *SmartLanguageDetector {
	return &SmartLanguageDetector{
		logger:            logger,
		workerCount:       getOptimalWorkerCount(),
		sampleSize:        8 * 1024, // 8KB sample - sufficient for most languages
		minConfidenceSize: 1 * 1024, // 1KB minimum for confidence
		// Language-distribution limit (linguist-style). Must match the parallel
		// and sequential paths so big files are skipped consistently.
		maxFileSize: LanguageStatsMaxFileSize,
	}
}

// ProcessTreeWithSampling processes tree files using intelligent sampling
func (sld *SmartLanguageDetector) ProcessTreeWithSampling(ctx context.Context, tree *object.Tree) (map[string]LanguageStats, int64, int, error) {
	// Create channels for work distribution
	fileChan := make(chan *object.File, 200)
	resultChan := make(chan SmartLanguageResult, 200)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < sld.workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			sld.smartFileWorker(ctx, fileChan, resultChan, workerID)
		}(i)
	}

	// Start result collector
	langStats := make(map[string]struct {
		bytes      int64
		files      int
		confidence float64
	})
	var totalBytes int64
	var totalFiles int
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)

	go func() {
		defer collectorWG.Done()
		for result := range resultChan {
			if result.Error != nil || result.Skipped {
				continue
			}

			// Weight results by confidence for better accuracy
			stats := langStats[result.Language]
			stats.bytes += result.Bytes
			stats.files += result.Files

			// Track confidence (higher confidence languages get more weight)
			if stats.confidence == 0 {
				stats.confidence = result.Confidence
			} else {
				// Average confidence with bias toward higher confidence
				stats.confidence = (stats.confidence + result.Confidence*1.2) / 2.2
			}

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
			sld.logger.Warn("Error iterating files", zap.Error(err))
		}
	}()

	// Wait for completion
	go func() {
		wg.Wait()
		close(resultChan)
	}()

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

	sld.logger.Debug("Smart language detection completed",
		zap.Int("total_files", totalFiles),
		zap.Int64("total_bytes", totalBytes),
		zap.Int("languages", len(finalStats)),
		zap.Int("worker_count", sld.workerCount))

	return finalStats, totalBytes, totalFiles, nil
}

// smartFileWorker processes files with intelligent sampling
func (sld *SmartLanguageDetector) smartFileWorker(ctx context.Context, fileChan <-chan *object.File, resultChan chan<- SmartLanguageResult, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case file, ok := <-fileChan:
			if !ok {
				return
			}

			result := sld.detectLanguageSmartly(ctx, file)

			select {
			case <-ctx.Done():
				return
			case resultChan <- result:
			}
		}
	}
}

// detectLanguageSmartly uses multi-stage detection with sampling
func (sld *SmartLanguageDetector) detectLanguageSmartly(ctx context.Context, file *object.File) SmartLanguageResult {
	// Stage 1: Basic filtering (same as before)
	if enry.IsVendor(file.Name) {
		return SmartLanguageResult{Skipped: true, SkipReason: "vendor"}
	}
	if enry.IsDotFile(file.Name) {
		return SmartLanguageResult{Skipped: true, SkipReason: "dotfile"}
	}
	if enry.IsDocumentation(file.Name) {
		return SmartLanguageResult{Skipped: true, SkipReason: "documentation"}
	}
	if enry.IsConfiguration(file.Name) {
		return SmartLanguageResult{Skipped: true, SkipReason: "configuration"}
	}

	// Stage 2: Smart size filtering
	if file.Size > sld.maxFileSize {
		return SmartLanguageResult{Skipped: true, SkipReason: "too_large"}
	}

	// Stage 3: Extension-based detection (fastest)
	if language, confident := sld.detectByExtension(file.Name); confident {
		return SmartLanguageResult{
			Language:   language,
			Bytes:      file.Size,
			Files:      1,
			Confidence: 0.9, // High confidence for extension-based detection
			Method:     DetectionByExtension,
		}
	}

	// Stage 4: Content sampling (much faster than full read)
	return sld.detectBySampling(file)
}

// detectByExtension provides fast detection based on file extension
func (sld *SmartLanguageDetector) detectByExtension(fileName string) (string, bool) {
	// Get language by filename
	language, _ := enry.GetLanguageByFilename(fileName)
	if language != "" {
		// Map to base language if needed
		if baseLanguage, exists := data.LanguagesGroup[language]; exists {
			language = baseLanguage
		}
		return language, true
	}
	return "", false
}

// detectBySampling uses intelligent content sampling for detection
func (sld *SmartLanguageDetector) detectBySampling(file *object.File) SmartLanguageResult {
	reader, err := file.Reader()
	if err != nil {
		return SmartLanguageResult{Skipped: true, SkipReason: "read_error"}
	}
	defer reader.Close()

	// OPTIMIZATION: Only read sample size, not entire file
	sampleBuffer := make([]byte, sld.sampleSize)
	bytesRead, err := reader.Read(sampleBuffer)
	if err != nil && err != io.EOF {
		return SmartLanguageResult{Skipped: true, SkipReason: "sample_read_error"}
	}

	// Use only the bytes we actually read
	sample := sampleBuffer[:bytesRead]

	// Check if binary
	if enry.IsBinary(sample) {
		return SmartLanguageResult{Skipped: true, SkipReason: "binary"}
	}

	// Check if generated (using sample)
	if enry.IsGenerated(file.Name, sample) {
		return SmartLanguageResult{Skipped: true, SkipReason: "generated"}
	}

	// Detect language from sample
	language := enry.GetLanguage(file.Name, sample)
	if language == "" {
		// Try filename-based detection as fallback
		language, _ = enry.GetLanguageByFilename(file.Name)
	}

	if language == "" {
		return SmartLanguageResult{Skipped: true, SkipReason: "unknown_language"}
	}

	// Map to base language
	if baseLanguage, exists := data.LanguagesGroup[language]; exists {
		language = baseLanguage
	}

	// Calculate confidence based on sample size vs file size
	confidence := sld.calculateConfidence(int64(bytesRead), file.Size)

	return SmartLanguageResult{
		Language:   language,
		Bytes:      file.Size,
		Files:      1,
		Confidence: confidence,
		Method:     DetectionBySampling,
	}
}

// calculateConfidence determines detection confidence based on sample coverage
func (sld *SmartLanguageDetector) calculateConfidence(sampleSize, fileSize int64) float64 {
	if sampleSize >= fileSize {
		return 1.0 // Full file read
	}

	if sampleSize >= int64(sld.minConfidenceSize) {
		// For files larger than 1KB, confidence decreases with size
		// but remains high for reasonable sampling ratios
		ratio := float64(sampleSize) / float64(fileSize)

		// Confidence formula: high for good sampling ratios
		if ratio >= 0.5 { // Sampled >= 50% of file
			return 0.95
		} else if ratio >= 0.1 { // Sampled >= 10% of file
			return 0.8 + (ratio * 0.3) // Scale between 0.8-0.95
		} else {
			return 0.6 + (ratio * 2.0) // Scale between 0.6-0.8
		}
	}

	// Low confidence for very small samples
	return 0.4
}

// getOptimalWorkerCount returns optimal worker count for language detection
func getOptimalWorkerCount() int {
	// Language detection is I/O bound, so use more workers than CPU cores
	workerCount := 12 // Good balance for I/O intensive work
	return workerCount
}

// Note: LanguageStats is already defined in parallel_tree.go

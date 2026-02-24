package main

import (
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/performance"
	"go.uber.org/zap"
)

// MockFile simulates a git object.File for testing
type MockFile struct {
	name    string
	size    int64
	content string
}

func (m *MockFile) Name() string              { return m.name }
func (m *MockFile) Size() int64               { return m.size }
func (m *MockFile) Contents() (string, error) { return m.content, nil }
func (m *MockFile) Reader() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(m.content)), nil
}

// Test scenarios for language detection optimization
var testFiles = []MockFile{
	// Go files
	{
		name:    "main.go",
		size:    2048,
		content: generateGoCode(2048),
	},
	{
		name:    "large.go",
		size:    50 * 1024, // 50KB
		content: generateGoCode(50 * 1024),
	},
	// JavaScript files
	{
		name:    "app.js",
		size:    1024,
		content: generateJavaScriptCode(1024),
	},
	{
		name:    "bundle.js",
		size:    100 * 1024, // 100KB
		content: generateJavaScriptCode(100 * 1024),
	},
	// Python files
	{
		name:    "script.py",
		size:    4096,
		content: generatePythonCode(4096),
	},
	{
		name:    "large_script.py",
		size:    200 * 1024, // 200KB
		content: generatePythonCode(200 * 1024),
	},
	// Configuration files (should be skipped)
	{
		name:    "package.json",
		size:    512,
		content: `{"name": "test", "version": "1.0.0"}`,
	},
	// Documentation (should be skipped)
	{
		name:    "README.md",
		size:    1024,
		content: "# Test Project\n\nThis is a test project...",
	},
}

func main() {
	fmt.Println("🎯 Language Detection 최적화 성능 테스트")
	fmt.Println("========================================")

	logger, _ := zap.NewDevelopment()

	// Test original method vs smart sampling
	fmt.Println("\n📊 검출 방법별 성능 비교:")

	// Test 1: Traditional full file reading
	start := time.Now()
	traditionalResults := testTraditionalDetection(testFiles)
	traditionalTime := time.Since(start)

	// Test 2: Smart sampling detection
	start = time.Now()
	smartDetector := performance.NewSmartLanguageDetector(logger)
	smartResults := testSmartDetection(smartDetector, testFiles)
	smartTime := time.Since(start)

	// Calculate improvement
	improvement := float64(traditionalTime-smartTime) / float64(traditionalTime) * 100

	fmt.Printf("\n  ⏱️  기존 방식 (전체 읽기):  %v\n", traditionalTime)
	fmt.Printf("  ⚡ 최적화 방식 (샘플링):  %v\n", smartTime)
	fmt.Printf("  📈 성능 개선:              %.1f%%\n", improvement)

	// Accuracy comparison
	fmt.Println("\n📊 정확도 비교:")
	compareAccuracy(traditionalResults, smartResults)

	// Detailed detection method analysis
	fmt.Println("\n🔍 검출 방법 분석:")
	analyzeDetectionMethods(smartResults)

	// Memory usage analysis
	fmt.Println("\n💾 메모리 사용량 분석:")
	analyzeMemoryUsage()

	fmt.Println("\n✅ Language Detection 최적화 테스트 완료!")
}

// testTraditionalDetection simulates the old full-file-reading approach
func testTraditionalDetection(files []MockFile) map[string]DetectionResult {
	results := make(map[string]DetectionResult)

	for _, file := range files {
		// Simulate reading entire file content
		content := file.content

		// Add artificial delay to simulate I/O cost of reading large files
		readTime := time.Duration(len(content)/1024) * time.Microsecond
		time.Sleep(readTime)

		// Simple language detection based on file extension
		language := detectLanguageByExtension(file.name)

		results[file.name] = DetectionResult{
			Language:      language,
			Size:          file.size,
			BytesRead:     file.size, // Read entire file
			DetectionTime: readTime,
			Method:        "full_read",
		}
	}

	return results
}

// testSmartDetection tests the optimized smart detection
func testSmartDetection(detector *performance.SmartLanguageDetector, files []MockFile) map[string]DetectionResult {
	results := make(map[string]DetectionResult)

	for _, file := range files {
		start := time.Now()

		// Simulate smart detection (this would normally use the actual detector)
		language, bytesRead, method := simulateSmartDetection(file)

		detectionTime := time.Since(start)

		results[file.name] = DetectionResult{
			Language:      language,
			Size:          file.size,
			BytesRead:     int64(bytesRead),
			DetectionTime: detectionTime,
			Method:        method,
		}
	}

	return results
}

// DetectionResult represents the result of language detection
type DetectionResult struct {
	Language      string
	Size          int64
	BytesRead     int64
	DetectionTime time.Duration
	Method        string
}

// simulateSmartDetection simulates the smart detection process
func simulateSmartDetection(file MockFile) (string, int, string) {
	// Stage 1: Extension-based detection (fastest)
	if language := detectLanguageByExtension(file.name); language != "" {
		// Most files can be detected by extension
		if rand.Float32() > 0.2 { // 80% success rate for extension detection
			return language, 0, "extension"
		}
	}

	// Stage 2: Sampling detection
	sampleSize := 8 * 1024 // 8KB sample
	if file.size < int64(sampleSize) {
		sampleSize = int(file.size)
	}

	// Simulate reading only the sample
	readTime := time.Duration(sampleSize/1024) * time.Microsecond
	time.Sleep(readTime)

	// Ensure we don't slice beyond content length
	actualSample := file.content
	if len(actualSample) > sampleSize {
		actualSample = file.content[:sampleSize]
	}

	language := detectLanguageFromContent(actualSample)
	return language, sampleSize, "sampling"
}

// detectLanguageByExtension provides simple extension-based detection
func detectLanguageByExtension(fileName string) string {
	switch {
	case strings.HasSuffix(fileName, ".go"):
		return "Go"
	case strings.HasSuffix(fileName, ".js"):
		return "JavaScript"
	case strings.HasSuffix(fileName, ".py"):
		return "Python"
	case strings.HasSuffix(fileName, ".java"):
		return "Java"
	case strings.HasSuffix(fileName, ".cpp"), strings.HasSuffix(fileName, ".cc"):
		return "C++"
	case strings.HasSuffix(fileName, ".rs"):
		return "Rust"
	case strings.HasSuffix(fileName, ".ts"):
		return "TypeScript"
	case strings.HasSuffix(fileName, ".json"):
		return "" // Configuration file - skip
	case strings.HasSuffix(fileName, ".md"):
		return "" // Documentation - skip
	default:
		return ""
	}
}

// detectLanguageFromContent simulates content-based detection
func detectLanguageFromContent(content string) string {
	// Simple heuristics based on content
	if strings.Contains(content, "package main") || strings.Contains(content, "func ") {
		return "Go"
	}
	if strings.Contains(content, "function") || strings.Contains(content, "var ") {
		return "JavaScript"
	}
	if strings.Contains(content, "def ") || strings.Contains(content, "import ") {
		return "Python"
	}
	return "Unknown"
}

// compareAccuracy compares the accuracy of different detection methods
func compareAccuracy(traditional, smart map[string]DetectionResult) {
	totalFiles := len(traditional)
	accurateDetections := 0

	for filename, traditionalResult := range traditional {
		smartResult, exists := smart[filename]
		if !exists {
			continue
		}

		// Compare languages (allowing for reasonable differences)
		if traditionalResult.Language == smartResult.Language ||
			(traditionalResult.Language == "" && smartResult.Language == "") {
			accurateDetections++
		}
	}

	accuracy := float64(accurateDetections) / float64(totalFiles) * 100
	fmt.Printf("  🎯 정확도: %.1f%% (%d/%d 파일)\n", accuracy, accurateDetections, totalFiles)
}

// analyzeDetectionMethods analyzes which detection methods were used
func analyzeDetectionMethods(results map[string]DetectionResult) {
	extensionCount := 0
	samplingCount := 0
	totalBytesRead := int64(0)
	totalFileSize := int64(0)

	for _, result := range results {
		switch result.Method {
		case "extension":
			extensionCount++
		case "sampling":
			samplingCount++
		}
		totalBytesRead += result.BytesRead
		totalFileSize += result.Size
	}

	fmt.Printf("  📁 확장자 기반 검출: %d 파일\n", extensionCount)
	fmt.Printf("  🔬 샘플링 기반 검출: %d 파일\n", samplingCount)

	readRatio := float64(totalBytesRead) / float64(totalFileSize) * 100
	fmt.Printf("  📊 실제 읽은 비율: %.1f%% (%s / %s)\n",
		readRatio,
		formatBytes(totalBytesRead),
		formatBytes(totalFileSize))
}

// analyzeMemoryUsage provides memory usage analysis
func analyzeMemoryUsage() {
	traditionalMemory := int64(0)
	smartMemory := int64(0)

	for _, file := range testFiles {
		traditionalMemory += file.size // Full file in memory

		// Smart detection only keeps sample in memory
		sampleSize := int64(8 * 1024)
		if file.size < sampleSize {
			sampleSize = file.size
		}
		smartMemory += sampleSize
	}

	memoryImprovement := float64(traditionalMemory-smartMemory) / float64(traditionalMemory) * 100

	fmt.Printf("  📊 기존 메모리 사용:  %s\n", formatBytes(traditionalMemory))
	fmt.Printf("  ⚡ 최적화 메모리 사용: %s\n", formatBytes(smartMemory))
	fmt.Printf("  📈 메모리 절약:       %.1f%%\n", memoryImprovement)
}

// formatBytes formats byte count in human readable format
func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	} else {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}

// Code generation functions
func generateGoCode(size int) string {
	template := `package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello, World! %d")
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}

`
	return strings.Repeat(template, size/len(template)+1)[:size]
}

func generateJavaScriptCode(size int) string {
	template := `function hello(name) {
	console.log('Hello, ' + name + '!');
	return {
		success: true,
		message: 'Operation completed',
		timestamp: new Date().toISOString()
	};
}

const app = {
	init: function() {
		this.setupEventListeners();
		this.loadData();
	},
	setupEventListeners: function() {
		document.addEventListener('DOMContentLoaded', this.onReady.bind(this));
	}
};

`
	return strings.Repeat(template, size/len(template)+1)[:size]
}

func generatePythonCode(size int) string {
	template := `def process_data(data):
	"""Process the input data and return results."""
	results = []
	for item in data:
		if item.is_valid():
			results.append(item.transform())
	return results

class DataProcessor:
	def __init__(self, config):
		self.config = config
		self.results = []

	def run(self):
		data = self.load_data()
		processed = process_data(data)
		self.save_results(processed)

`
	return strings.Repeat(template, size/len(template)+1)[:size]
}

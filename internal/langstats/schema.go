package langstats

// === API 응답 타입 ===

// LanguageStatsResponse는 언어 통계 API 응답 구조체입니다.
type LanguageStatsResponse struct {
	Success    bool            `json:"success"`
	Repository string          `json:"repository"`
	CommitSHA  string          `json:"commit_sha"`
	Languages  []LanguageStat  `json:"languages"`
	TotalBytes int64           `json:"total_bytes"`
	TotalFiles int             `json:"total_files"`
	Meta       ComputationMeta `json:"meta"`
}

// LanguageStat은 개별 언어의 통계 정보입니다.
type LanguageStat struct {
	Language   string  `json:"language"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
	Files      int     `json:"files"`
}

// ComputationMeta는 연산 메타데이터입니다.
type ComputationMeta struct {
	TotalFilesScanned  int `json:"total_files_scanned"`
	FilesSkipped       int `json:"files_skipped"`
	FilesDetectedByExt int `json:"files_detected_by_ext"`
	FilesSampled       int `json:"files_sampled"`
	ElapsedMs          int `json:"elapsed_ms"`
}

// === 내부 타입 (Phase 1 → Phase 2 전달용) ===

// langAccum은 언어별 누적 통계입니다.
type langAccum struct {
	bytes int64
	files int
}

// sampleCandidate는 샘플링 대상 파일 정보입니다.
type sampleCandidate struct {
	hash string
	path string
	size int64
}

// extensionGroup은 모호한 확장자 그룹의 정보입니다.
type extensionGroup struct {
	candidates []string // enry가 반환한 언어 후보 목록
	totalBytes int64
	totalFiles int
	samples    []sampleCandidate // capped at maxSamplesPerExt
}

// scanResult는 Phase 1 스캔 결과입니다.
type scanResult struct {
	langStats       map[string]*langAccum
	ambiguousGroups map[string]*extensionGroup // key: extension
	totalScanned    int
	totalSkipped    int
}

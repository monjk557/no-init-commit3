package blob

// LineStats 는 blob 콘텐츠의 라인 메트릭입니다.
type LineStats struct {
	// LineCount 는 총 라인 수. 마지막 줄에 trailing newline 이 없어도
	// 1 로 카운트 (기존 /overview/blob 의 동작과 일치).
	LineCount int

	// MaxLineLength 는 가장 긴 라인의 바이트 수 (\n, \r 제외).
	MaxLineLength int
}

// ComputeLineStats 는 blob 바이트열을 한 번만 순회하여 라인 통계를
// 계산합니다.
//
// 기존 코드 (`internal/overview/handler.go`) 는
//
//	strings.Count(strings.ReplaceAll(content, "\r\n", "\n"), "\n") + 1
//
// 형태로 ReplaceAll 이 전체 콘텐츠를 한 번 더 복제했다. 783KB 파일이면
// 1.5MB 임시 할당이 발생. 이 함수는 단일 패스로 \r\n / \n 양쪽을 처리하고
// MaxLineLength 까지 같이 산출하여 메모리 스파이크와 중복 순회를 모두
// 제거한다 (Issue-3144).
//
// 빈 바이트열에 대해 LineCount=0, MaxLineLength=0 을 반환한다. 기존 동작
// (빈 string 에서 Count("\n")+1 = 1) 과는 다르지만, 호출부에서 콘텐츠가
// 빈 경우 별도로 처리하므로 안전.
func ComputeLineStats(b []byte) LineStats {
	if len(b) == 0 {
		return LineStats{}
	}

	var (
		lineCount     int
		maxLineLength int
		current       int
	)

	for _, c := range b {
		switch c {
		case '\n':
			if current > maxLineLength {
				maxLineLength = current
			}
			lineCount++
			current = 0
		case '\r':
			// \r\n 의 \r 또는 lone \r — 라인 길이에 포함하지 않음.
			// \r 만 있는 Mac OS Classic 스타일은 거의 멸종이므로 \n
			// 기반 카운팅을 유지한다.
		default:
			current++
		}
	}

	// 마지막 라인이 trailing newline 없이 끝난 경우.
	if current > 0 {
		if current > maxLineLength {
			maxLineLength = current
		}
		lineCount++
	}

	return LineStats{
		LineCount:     lineCount,
		MaxLineLength: maxLineLength,
	}
}

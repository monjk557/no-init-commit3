// Package lfs provides LFS pointer file parsing utilities.
//
// LFS HTTP API (batch / verify / locks / usage) 는 orchestrator + api-service 에
// 위임된다. git-executor 는 git tree 안의 LFS pointer blob 을 인식 / 파싱하는
// 순수 함수만 제공한다 (overview preview, GC reachability scan 에서 사용).
package lfs

import (
	"regexp"
	"strconv"
	"strings"
)

// lfsPointerPattern 은 LFS pointer file 의 정규식 패턴입니다.
var lfsPointerPattern = regexp.MustCompile(`^version https://git-lfs\.github\.com/spec/v1\noid sha256:([a-f0-9]{64})\nsize (\d+)\n$`)

// IsLFSPointer 는 콘텐츠가 LFS pointer file 인지 확인합니다.
func IsLFSPointer(content []byte) bool {
	return lfsPointerPattern.Match(content)
}

// ParseLFSPointer 는 LFS pointer 에서 OID 와 크기를 추출합니다.
func ParseLFSPointer(content []byte) (oid string, size int64, ok bool) {
	matches := lfsPointerPattern.FindSubmatch(content)
	if matches == nil {
		return "", 0, false
	}

	oid = string(matches[1])
	size, err := strconv.ParseInt(string(matches[2]), 10, 64)
	if err != nil {
		return "", 0, false
	}

	return oid, size, true
}

// FormatLFSPointer 는 LFS pointer file 콘텐츠를 생성합니다.
func FormatLFSPointer(oid string, size int64) string {
	var b strings.Builder
	b.WriteString("version https://git-lfs.github.com/spec/v1\n")
	b.WriteString("oid sha256:")
	b.WriteString(oid)
	b.WriteString("\n")
	b.WriteString("size ")
	b.WriteString(strconv.FormatInt(size, 10))
	b.WriteString("\n")
	return b.String()
}

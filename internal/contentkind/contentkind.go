// Package contentkind 는 "이 파일을 텍스트로 다룰 수 있는가" 판정을 한 곳에
// 모은다.
//
// 이전에는 blob / diffs / merge / langstats 가 각자 판정을 들고 있었고 규칙이
// 서로 달랐다 — 같은 파일이 blob view 에서는 바이너리, diff 에서는 텍스트로
// 보이는 불일치가 실제로 가능했다. 판정 기준이 도메인마다 다르면 사용자는
// "왜 여기선 보이고 저기선 안 보이지" 를 설명할 방법이 없다.
package contentkind

import (
	"path/filepath"
	"strings"

	"github.com/go-enry/go-enry/v2"
)

// SniffBytes 는 판정에 쓰는 선행 바이트 수다. git 자신도, linguist 도 앞
// 8000 바이트 안의 NUL 로 판정한다 — 파일이 아무리 커도 판정 비용은 상수다.
const SniffBytes = 8 * 1024

// IsBinary 는 주어진 바이트(파일 앞부분이면 충분하다)가 바이너리인지 판정한다.
//
// enry(= GitHub linguist 룰)의 NUL 바이트 기준을 쓴다. UTF-8 유효성 검사는
// 쓰지 않는다 — 앞부분만 넘길 때 멀티바이트 룬이 경계에서 잘리면 유효하지
// 않게 보여, 8KB 넘는 한글/CJK 소스가 바이너리로 오판된다. 또 latin-1 /
// CP949 처럼 UTF-8 이 아닐 뿐 텍스트인 파일도 살린다.
func IsBinary(head []byte) bool {
	if len(head) > SniffBytes {
		head = head[:SniffBytes]
	}
	return enry.IsBinary(head)
}

// binaryExtensions 는 내용을 읽지 않고 경로만으로 바이너리를 추정할 때 쓰는
// 목록이다. diffs / merge 가 각각 들고 있던 두 목록의 합집합.
//
// 내용이 있으면 IsBinary 를 쓸 것 — 이 함수는 diff 처럼 blob 을 열지 않고
// 경로만 아는 자리를 위한 근사다.
var binaryExtensions = map[string]bool{
	// 이미지
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".ico": true, ".webp": true, ".svg": true, ".tiff": true, ".heic": true,
	// 문서 / 압축
	".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".bz2": true,
	".xz": true, ".7z": true, ".zst": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	// 실행/라이브러리/오브젝트
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".jar": true, ".war": true, ".ear": true, ".class": true,
	".pyc": true, ".pyo": true, ".o": true, ".a": true, ".lib": true,
	// 폰트
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	// 미디어
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true, ".wav": true,
	// 디스크 / DB 이미지
	".db": true, ".sqlite": true, ".sqlite3": true,
	".dmg": true, ".iso": true, ".img": true,
}

// IsBinaryPath 는 확장자만으로 바이너리 여부를 추정한다. 확장자가 없으면
// false — 내용을 못 본 상태에서 단정하지 않는다.
func IsBinaryPath(path string) bool {
	return binaryExtensions[strings.ToLower(filepath.Ext(path))]
}

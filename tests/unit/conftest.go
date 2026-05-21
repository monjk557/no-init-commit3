package unit

import (
	"os"
)

// init 함수는 패키지가 로드될 때 자동으로 실행되어 테스트 환경변수를 설정합니다.
// Python의 conftest.py와 유사한 역할을 수행합니다.

func init() {
	SetupTestEnv()
}

// SetupTestEnv는 모든 unit 테스트에서 사용할 환경변수를 설정합니다.
// 이 함수는 init()에서 자동으로 호출되지만, 필요시 직접 호출할 수도 있습니다.
func SetupTestEnv() {
	// Set test environment variables FIRST - before any app imports
	// 이 환경변수들은 모든 unit 테스트에서 사용됩니다.

	// Server Configuration - HTTP API 서버 설정 (doc-360 §4.7 self-prefix 제거)
	_ = os.Setenv("PORT", "5001")
	_ = os.Setenv("TCP_GIT_PORT", "5222")
	_ = os.Setenv("BASE_PATH", "/")
	_ = os.Setenv("HOST", "0.0.0.0") // 코드 미사용이나 helm/env 일관성 위해 권장명

	// Git Repository Configuration - Git 저장소 루트 경로
	_ = os.Setenv("GIT_REPO_ROOT", "/var/lib/git/repos")
}

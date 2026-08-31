// log-guard 프로브 — 이 레포의 실제 로거로 대표 로그를 stdout 에 emit 한다.
//
// `make guard-logshape` 가 이 파일을 실행하고 출력을 .collabops/log.schema.json
// (collabops-code-harness 계약 사본)으로 검증한다. **앱은 띄우지 않는다** —
// 로거 부트스트랩만 태우므로 DB·다운스트림이 필요 없다.
package main

import (
	"errors"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

// 가드가 출력에서 이 값을 찾는다 — 나오면 마스킹이 뚫린 것이다.
const probeSecret = "probe-secret-do-not-log"

func main() {
	l, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		panic(err)
	}
	log := l.Logger

	log.Info("worker_started") // 최소 라인 (비-HTTP, event 미지정 → msg 로 채움)

	log.Info("http request", // 2xx
		zap.String("event", "http_request"),
		zap.String("method", "GET"),
		zap.String("path", "/repos/acme/x"),
		zap.Int("status_code", 200),
		zap.Float64("duration_ms", 12.4),
	)

	log.Warn("http error", // 4xx → warning (§5)
		zap.String("event", "http_error"),
		zap.String("method", "GET"),
		zap.String("path", "/repos/acme/x/x"),
		zap.Int("status_code", 404),
		zap.String("error_code", "RESOURCE_NOT_FOUND"),
	)

	log.Error("http error", // 5xx → error (§5)
		zap.String("event", "http_error"),
		zap.String("method", "POST"),
		zap.String("path", "/repos/acme/x"),
		zap.Int("status_code", 503),
		zap.String("error_code", "EXTERNAL_SERVICE_ERROR"),
	)

	// 민감정보 마스킹 + 계측 필드 보존.
	log.Error("login failed",
		zap.String("event", "http_error"),
		zap.String("method", "POST"),
		zap.String("path", "/api/v1/auth/login"),
		zap.Int("status_code", 500),
		zap.String("error_code", "INTERNAL_ERROR"),
		zap.String("password", probeSecret),
		zap.Float64("jwt_ms", 12.3),
		zap.Error(errors.New("boom")),
	)
}

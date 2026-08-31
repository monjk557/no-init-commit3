// 로그 라인 계약 (Issue-8015) — zap 인코더 경계에서 강제한다.
//
// 계약 본문 SoT 는 collabops-code-harness 의 configs/log.schema.json 이고, 이 레포는
// .collabops/log.schema.json 에 그 사본을 둔다(harness 가 sha256 을 대조한다).
//
// **왜 인코더인가.** 호출부마다 지키게 하면 하나만 빠뜨려도 규격을 벗어나고, 그건
// 소스 grep 으로 안 보인다. 모든 로그 줄이 반드시 지나는 인코더 한 지점에서 처리하면
// 어느 호출부로 들어오든 걸린다 — Python 서비스들이 structlog processor 경계에서
// 하는 것과 같은 구조다.
package logging

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// contractTopLevel 은 계약이 top-level 로 허용하는 필드다. 그 밖의 필드는 metadata 로
// 접는다(계약 §2 — 값을 버리지 않는다).
var contractTopLevel = map[string]struct{}{
	"event": {}, "level": {}, "msg": {},
	"request_id": {}, "trace_id": {}, "span_id": {},
	"method": {}, "path": {},
	"error_code": {}, "status_code": {},
	"metadata": {}, "duration_ms": {},
	"request_size_bytes": {}, "request_content_type": {}, "request_body_keys": {},
	"exc_info": {},
}

// sensitiveKeyParts — 키 이름에 이 조각이 들어 있으면 값을 가린다(Issue-8093).
// 부분 일치라 user_password / X-Api-Key / refresh_token 을 함께 잡는다.
var sensitiveKeyParts = []string{
	"password", "passwd", "secret", "token", "authorization",
	"api_key", "apikey", "private_key", "credential", "cookie",
	"session_id", "otp",
}

// measurementSuffixes — 이 이름으로 끝나면서 값이 숫자면 자격증명일 수 없다.
// 부분 일치 마스킹이 계측 필드까지 지우는 것을 막는다(app-web 에서 jwt_ms 가 전량
// [REDACTED] 된 실측 사례).
var measurementSuffixes = []string{"_ms", "_bytes", "_count", "_size", "_len", "_sec"}

const redacted = "[REDACTED]"

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, suffix := range measurementSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// contractEncoder 는 zap 인코더를 감싸 계약을 강제한다.
//
// fixedMeta 는 모든 줄의 metadata 에 실릴 고정 필드다(service·version 등).
// zap 의 `logger.With(...)` 는 필드를 **인코더에 구워 넣어** EncodeEntry 의 fields
// 로 오지 않는다. 그래서 With 로 붙인 전역 필드는 접기 대상이 되지 못하고 top-level
// 로 새어 나간다 — 실측으로 확인했다. 고정 필드는 여기로 넘겨야 한다.
type contractEncoder struct {
	zapcore.Encoder
	fixedMeta []zapcore.Field
}

// NewContractEncoder 는 계약을 강제하는 JSON 인코더를 만든다.
//
// EncoderConfig 는 여기서 계약에 맞게 고정한다 — 호출부가 TimeKey/CallerKey 를
// 임의로 켜면 top-level 에 계약 밖 필드가 새어 나가기 때문이다.
func NewContractEncoder(fixedMeta ...zapcore.Field) zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.MessageKey = "msg"
	cfg.LevelKey = "level"
	cfg.EncodeLevel = contractLevelEncoder
	// timestamp·caller 는 계약 top-level 목록에 없다. 버리지 않고 EncodeEntry 에서
	// metadata 로 내린다(Loki 가 자체 타임스탬프를 갖지만 caller 는 진단에 쓰인다).
	cfg.TimeKey = zapcore.OmitKey
	cfg.CallerKey = zapcore.OmitKey
	cfg.NameKey = zapcore.OmitKey
	cfg.StacktraceKey = "exc_info"
	return contractEncoder{Encoder: zapcore.NewJSONEncoder(cfg), fixedMeta: fixedMeta}
}

// contractLevelEncoder — 계약의 level enum 은 warning 이다. zap 기본은 warn 이라
// 그대로 두면 전 서비스 단일 쿼리(`level="warning"`)에서 이 서비스만 빠진다.
func contractLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	if l == zapcore.WarnLevel {
		enc.AppendString("warning")
		return
	}
	enc.AppendString(l.String())
}

func (e contractEncoder) Clone() zapcore.Encoder {
	return contractEncoder{Encoder: e.Encoder.Clone(), fixedMeta: e.fixedMeta}
}

// EncodeEntry — 계약 필드는 top-level 에 두고 나머지는 metadata 로 접는다.
func (e contractEncoder) EncodeEntry(
	ent zapcore.Entry, fields []zapcore.Field,
) (*buffer.Buffer, error) {
	top := make([]zapcore.Field, 0, len(fields)+1)
	meta := make([]zapcore.Field, 0, len(fields)+2)
	hasEvent := false

	for _, f := range fields {
		if isSensitiveKey(f.Key) {
			f = zap.String(f.Key, redacted)
		}
		if _, ok := contractTopLevel[f.Key]; ok {
			if f.Key == "event" {
				hasEvent = true
			}
			top = append(top, f)
			continue
		}
		meta = append(meta, f)
	}

	// event 는 계약 필수다. 호출부가 안 주면 메시지를 쓴다 — 키를 빼면 계약 위반이고,
	// 빈 문자열을 넣으면 "무슨 사건인지" 를 영영 알 수 없다.
	if !hasEvent {
		top = append(top, zap.String("event", ent.Message))
	}

	meta = append(meta, e.fixedMeta...)
	meta = append(meta, zap.String("timestamp", ent.Time.UTC().Format(timeLayout)))
	if ent.Caller.Defined {
		meta = append(meta, zap.String("caller", ent.Caller.TrimmedPath()))
	}

	// top 을 그대로 재사용하지 않고 새 슬라이스에 담는다 — append 결과를 다른
	// 변수에 넣으면 top 의 backing array 를 공유해 호출부가 보는 값이 바뀔 수 있다.
	out := make([]zapcore.Field, 0, len(top)+len(meta)+1)
	out = append(out, top...)
	out = append(out, zap.Namespace("metadata"))
	out = append(out, meta...)

	buf, err := e.Encoder.EncodeEntry(ent, out)
	if err != nil {
		return nil, fmt.Errorf("계약 인코딩 실패: %w", err)
	}
	return buf, nil
}

const timeLayout = "2006-01-02T15:04:05.000Z"

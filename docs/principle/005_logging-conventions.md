# 005. 로깅 컨벤션

## 개요

- JSON 로그 포맷, 데브옵스 표준 필드 사용.
- HTTP 요청은 미들웨어에서 한 줄로 로깅하며, 비즈니스 로그는 `event` + `request_id`로 요청 단위 추적 가능.

## 공통 로그 필드

| 필드 | 설명 |
|------|------|
| `level` | 로그 레벨 (info, warn, error 등) |
| `msg` | 로그 메시지 (zap 기본 메시지 키는 `msg`) |
| `timestamp` | ISO8601 시각 |
| `caller` / `function` | 호출 위치 (개발/디버깅용) |

## HTTP 요청 로그 (GinRequestLogger)

`GET /health` 를 제외한 모든 HTTP 요청에 대해 미들웨어에서 한 번만 로깅한다.

- **포함 필드**: `event`, `request_id`, `global_trace_id`, `method`, `path`, `duration_ms`, `http`(status), `request_size_bytes`, `request_content_type`, `request_body_keys`, `metadata`
- **event**: `http_request` 고정
- **request_id**: `X-Request-ID` 헤더 또는 자동 생성값. 응답 헤더에도 동일 값 설정.
- **global_trace_id**: `X-Global-Trace-ID` 헤더 값 (있을 경우)
- **request_body_keys**: JSON body의 최상위 키 목록 (POST/PUT/PATCH, 64KB 이하, application/json만)

### Health check 미로깅

- 경로가 `/health` 또는 `/health/*` 인 요청은 로그를 남기지 않는다.
- 예: `/health`, `/health/liveness`, `/health/readiness`

## 비즈니스 로그 (핸들러/서비스)

- **context 전달**: HTTP 핸들러에서는 `c.Request.Context()` 를 서비스로 넘긴다. 미들웨어에서 이미 `request_id`(와 `global_trace_id`)가 context에 주입되어 있다.
- **로깅 방법**: `logger.LogInfo(ctx, "메시지", zap.String("event", "이벤트명"), ...필드)` 사용.
- **event**: 로그 종류 식별용 (예: `branch_created`, `branch_deleted`, `branch_update_request`).
- **request_id**: `LogInfo`/`LogWarning`/`LogError` 내부에서 context의 `request_id`를 읽어 자동으로 필드에 포함.

### 예시

```go
// 서비스에서
s.logger.LogInfo(ctx, "Branch created successfully",
    zap.String("event", "branch_created"),
    zap.String("branch", req.Name),
    zap.String("commit_sha", sourceCommit.String()))

// 핸들러에서
h.logger.LogInfo(c.Request.Context(), "Processing branch creation request",
    zap.String("event", "branch_create_request"),
    zap.String("owner", owner),
    zap.String("repo", repo))
```

## Context 키 (logging 패키지)

- `RequestIDKey` ("request_id"): 요청 ID. GinRequestLogger가 주입.
- `CorrelationIDKey` ("correlation_id"): 전역 트레이스 ID 등 상관 ID. `X-Global-Trace-ID` 가 있으면 주입.
- `SkipLoggingKey` ("skip_logging"): 표준 net/http 미들웨어용. Gin 경로 기반 스킵은 GinRequestLogger 내부에서 `/health` 로 처리.

## 설정

- `config.LoggingConfig`: `level`, `format`(json/text), `output`(stdout/stderr/파일경로), `development`.
- JSON 포맷 권장 (수집/검색용). 메시지 키는 `msg` 로 통일.

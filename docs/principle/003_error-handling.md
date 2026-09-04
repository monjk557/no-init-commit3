# 003. 에러 핸들링 규칙

## 핵심 원칙

**에러는 `ErrorCodeEnum` 코드를 기준으로 삼아, 중앙 카탈로그(`internal/api/errcatalog.go`)를 통해 응답한다.**

과거에는 핸들러마다 `c.JSON()`으로 직접 status를 고르거나, 도메인별로 제각각인 매핑 함수를 하나씩 만들어 썼다. 그러다 보니 같은 종류의 에러라도 핸들러마다 status 코드가 다르게 나가고, 클라이언트(orchestrator, 웹, CLI)는 텍스트 메시지를 파싱해서 에러 종류를 추측해야 했다. 에러 코드가 이렇게 산재되어 있으면 신뢰할 수 있는 단일 기준이 없어지므로, `code`라는 추상화 하나를 추가해서 이 문제를 해결했다. 핸들러는 `code`만 고르고, `code`가 어떤 status·메시지·재시도 가능 여부를 갖는지는 카탈로그가 결정한다.

이 구조를 두는 목적은 명확하다. git-executor 자신은 사용자에게 보여줄 메시지를 만들지 않는다. `code`와 `params`만 실어 보내면, orchestrator가 그 코드를 `GIT_<code>` 형태로 조회해서 최종 사용자에게 보여줄 친절한 메시지로 번역한다. 즉 이 카탈로그는 executor 내부의 일관성뿐 아니라, 최종 사용자가 받아 보는 에러 메시지의 품질까지 좌우하는 지점이다.

---

## 구조

### 1. `ErrorCodeEnum` — 코드의 단일 진실 원천

`internal/api/schema.go`에 정의되어 있다. OpenAPI spec generator가 이 파일을 스캔해서 `components/schemas/ErrorCodeEnum`으로 뽑아내므로, 여기 없는 코드는 스펙에도 없다.

```go
const (
    ErrCodeRefNotFound   ErrorCodeEnum = "REF_NOT_FOUND"
    ErrCodeMergeConflict ErrorCodeEnum = "MERGE_CONFLICT"
    // ...
)
```

새 에러 코드를 응답에 추가하려면 반드시 여기에도 추가해야 한다. orchestrator 쪽 카탈로그 완전성 guard가 스펙에 없는 코드를 CI에서 막는다.

### 2. `errorCatalog` — code → HTTP 표현 매핑

`internal/api/errcatalog.go`에 `map[ErrorCodeEnum]catalogEntry`로 존재한다. 각 code는 `Status`(HTTP status), `Message`(기본 메시지), `Retryable`(재시도 가능 여부)을 갖는다.

```go
var errorCatalog = map[ErrorCodeEnum]catalogEntry{
    ErrCodeRefNotFound:   {http.StatusNotFound, "reference not found", false},
    ErrCodeMergeConflict: {http.StatusConflict, "merge conflict", false},
    // ...
}
```

**불변식: 어떤 code도 422(Unprocessable Entity)로 매핑하지 않는다.** git-executor의 git 엔드포인트는 대부분 body 없는 GET이라, 422를 쓰면 서버 측 상태(없음/손상/빈 repo)를 클라이언트 잘못으로 오분류하게 된다. 없음은 404로, 빈 repo는 200으로, 손상되었거나 예기치 못한 실패는 500으로 나눠서 표현한다. `TestCatalogHasNo422`가 이 불변식을 CI에서 강제한다.

### 3. `NewCoded` — 핸들러의 표준 에러 생성 경로

핸들러는 status를 직접 고르지 않고, `api.NewCoded(code, ...)`로 code만 고른다.

```go
if err != nil {
    return MergeResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound, err.Error())
}
```

`NewCoded`는 카탈로그에서 status와 기본 메시지를 가져와 `*typed.HTTPError`를 만든다. `overrideMsg`를 주면 사용자에게 보여줄 메시지 텍스트만 바꾸고, status와 code는 카탈로그 값을 그대로 유지한다. 등록되지 않은 code는 500(`internal error`)으로 안전하게 강등되고, 절대 panic하지 않는다.

파라미터 보간이 필요하면 `NewCodedWithParams`를 쓴다. 예를 들어 `MERGE_CONFLICT`는 `source_ref`, `target_ref`, `conflict_count`를 params로 함께 실어서, orchestrator가 `GIT_MERGE_CONFLICT` 카탈로그 메시지에 이 값들을 채워 넣을 수 있게 한다.

### 4. Git 에러 자동 분류

`internal/api/giterror.go`의 `ClassifyGitError(err)`는 go-git이나 git CLI가 던지는 에러 메시지를 문자열 패턴으로 검사해서 적절한 `ErrorCodeEnum`으로 변환한다. `context.DeadlineExceeded`는 `ErrCodeTimeout`으로, `"not a git repository"` 계열 메시지는 `ErrCodeRepositoryNotFound`로 매핑하는 식이다. 저수준 git 에러를 code로 옮기는 진입점이라, 새로운 종류의 git 에러를 다뤄야 하면 여기에 분류 규칙을 추가한다.

---

## 예외: Protocol 레이어

**Git Protocol, SSH Protocol처럼 REST가 아닌 프로토콜에서는 자체 `ErrorHandler`를 그대로 쓴다.**

`internal/protocol/errors.go`의 `ErrorHandler.HandleError()`가 여전히 담당한다. Git protocol은 pkt-line 같은 특수한 에러 포맷이 필요해서, REST 응답 카탈로그와 같은 방식으로 처리할 수 없다. 이 레이어는 orchestration 레이어에 해당하므로 중앙집중식 처리가 오히려 맞다.

---

## 새 에러를 추가할 때

1. 이미 있는 code로 표현할 수 있는지 먼저 확인한다. 비슷한 의미의 code를 새로 만들지 않는다.
2. 정말 새로운 종류의 에러라면 `schema.go`에 `ErrorCodeEnum` 상수를 추가한다 (알파벳 순 유지).
3. `errcatalog.go`의 `errorCatalog` 맵에 `{Status, Message, Retryable}`을 등록한다. 422는 쓰지 않는다.
4. 핸들러에서는 `api.NewCoded(code, ...)` 또는 `api.NewCodedWithParams(code, message, params)`로 반환한다. `c.JSON()`을 직접 호출하거나 도메인별 매핑 함수를 새로 만들지 않는다.
5. `make specs`로 OpenAPI 스펙을 함께 갱신하고 커밋에 포함시킨다.

---

## 참고 문서

- `_archive/error_handling_guidelines.md` — "매핑 함수/중앙 카탈로그 금지, 핸들러 직접 응답"을 원칙으로 삼았던 이전 버전. 지금 구조와 정반대라 archive로 옮겼다.
- `_archive/gophercon/error_mapper_analysis.md` — 위 이전 버전의 근거가 됐던 분석. "핸들러 90%가 직접 응답" 시점의 코드베이스를 기준으로 한 것이라 지금과 다르다.

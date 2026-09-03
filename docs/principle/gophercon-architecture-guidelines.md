# GopherCon 기반 Go 아키텍처 원칙

이 문서는 GopherCon에서 다룬 Go 설계 원칙 중 우리 코드베이스에 실제로 적용하는 부분을 정리한 것이다. 코드를 새로 짜거나 리뷰할 때 기준으로 삼는 문서이고, 원본 세션 노트와 프로젝트별 준수 분석은 `_archive/gophercon/`에 남겨뒀다 — 근거를 다시 찾아보고 싶으면 거기서 확인하면 된다.

먼저 짚어둘 게 있다. 아래 원칙 중 net/http·gin·small interface·accept-interface-return-struct·options pattern 같은 것들은 Rob Pike의 Go Proverbs, Effective Go, Google/Uber/Kubernetes 사례에서 반복적으로 검증된 내용이라 그대로 기준으로 써도 된다. 반면 Effect 패턴은 발표자 개인의 실험적 제안에 가깝고 Go 커뮤니티 표준도 아니라서, "표준"이 아니라 "제한적으로 고려해볼 도구" 정도로 취급한다. 이 구분을 지우고 다 같은 무게로 취급하지 않는 게 중요하다.

## 1. HTTP 프레임워크

일반적인 서비스에는 gin 또는 net/http가 안전한 선택이다. 극단적인 성능이 필요할 때만 fiber/fasthttp를 고려하되, fiber는 `c.Params()` 같은 컨텍스트 반환값이 내부 버퍼를 재사용하므로 요청 스코프를 벗어나 쓸 거면(예: goroutine에 넘길 때) 반드시 복사해야 한다. 우리는 gin을 쓰고 있고, 이건 가이드라인에 부합한다.

```go
// bad: 버퍼 참조를 그대로 goroutine에 넘김
result := c.Params("foo")
go func() { useString(result) }()

// good: 경계를 넘길 때 복사
buf := make([]byte, len(result))
copy(buf, result)
go func() { useString(string(buf)) }()
```

## 2. 최소 추상 철학

Go는 함수형 언어가 아니다. 순수 함수와 부수 효과(side effect)를 실용적으로 나눌 뿐이다. 이 철학은 세 영역에서 일관되게 나타난다.

- **동시성**: goroutine과 channel이라는 최소 도구만 제공한다.
- **객체 모델**: struct, method, interface, embedding으로 제한한다. 상속 대신 조합.
- **에러 처리**: 예외가 아니라 다중 반환값으로 명시적으로 드러낸다.

실무 기준은 이렇다: 입력과 출력만으로 설명되는 로직은 순수 함수로 유지하고, 시간·I/O·네트워크·goroutine 생성은 "경계"로 밀어낸다. 순수 로직 테스트는 `t.Parallel()`로 돌릴 수 있어야 한다.

다만 이 최소주의에는 대가가 있다 — goroutine과 channel이 복잡하게 얽히면 cancellation, graceful shutdown, 테스트 안정성 확보가 급격히 어려워진다. 이건 Go의 결함이 아니라 최소 추상을 그대로 노출한 채 복잡한 도메인을 다루려 할 때 생기는 문제다. 그래서 동시성 도구는 아래처럼 상황별로 고른다.

| 상황 | 도구 |
|---|---|
| 단순 병렬 처리 | worker pool |
| 상태 공유 (read-heavy) | sync.Map |
| 조건 기반 대기/신호 | sync.Cond (channel로 안 될 때만) |
| 여러 goroutine 생명주기 묶기 | errgroup / run.Group |

sync.Cond는 강력하지만 디버깅이 어려워서 공용 유틸이나 캡슐화된 구조 안에서만 쓴다.

## 3. 인터페이스는 설계하지 말고 발견한다

Small Interface 원칙이 핵심이다 — 인터페이스가 커질수록 추상화는 약해진다.

- 인터페이스는 소비자(사용하는 쪽)가 정의한다. 제공자 쪽에 미리 만들어두지 않는다.
- 반환값은 구조체로, 입력은 인터페이스로 받는다("accept interfaces, return structs").
- 인터페이스는 미리 설계하는 게 아니라 중복과 사용 패턴이 실제로 드러났을 때 뽑아낸다. 대충 3회 이상 반복되면 그때 고려한다.
- 인터페이스에 메서드를 추가할 때는 테스트 영향 범위를 먼저 확인한다.
- 생성자 파라미터가 3개를 넘어가면 options pattern을 고려한다 (telescoping constructor 방지).

## 4. 에러 응답: 직접 응답이 기본, 매핑 함수는 필요할 때만

지금 코드베이스는 핸들러의 90% 이상이 `c.JSON()`으로 직접 응답한다. 이건 의도한 결정이고 앞으로도 기본값으로 유지한다.

- **기본**: 핸들러에서 바로 `c.JSON(status, gin.H{...})`. 명시적이고 디버깅이 쉽고 최소 추상 철학에 맞는다.
- **도메인별 매핑 함수**: 같은 도메인에서 에러→상태코드 매핑이 3회 이상 반복되거나 로직이 단순 if문을 넘어서면 그때 뽑아낸다. `merge/handler.go`의 `statusCodeForResolveReferenceError()`가 이 패턴의 예다.
- **중앙집중식 ErrorHandler**: 전체 핸들러에 일괄 적용하지 않는다. `protocol/errors.go`처럼 Git/SSH protocol 같은 orchestration 레이어에서만 쓴다 — 이런 레이어는 pkt-line 같은 특수 에러 포맷이 필요해서 중앙 처리가 실제로 이득이다.
- **미사용 추상화는 남겨두지 않는다**: 정의만 있고 안 쓰는 헬퍼(`WrapRepositoryError()` 같은)는 제거하거나, 필요해질 때 다시 만든다. "언젠가 쓸지도 모르니" 남겨두지 않는다.

중앙집중식 ErrorHandler를 전면 도입하는 건 비권장이다 — 최소 추상 철학에 어긋나고, 지금 코드베이스 관례와도 안 맞고, 팀이 새로 배워야 할 게 늘어난다.

## 5. Effect 패턴은 표준이 아니라 제한적 실험

Effect란 입력·출력만으로 표현 안 되는 작업(I/O, 시간, 네트워크, 동시성)을 즉시 실행하지 않고 선언된 값으로 다뤄서 실행 시점을 위임하는 개념이다. Effect handler가 동시성 제어·실행 순서·자원 관리를 한곳에서 맡는다.

이건 함수형 언어에서 오래된 패턴이고, Go에서는 "부수 효과를 도메인 로직 밖으로 밀어내려는 시도" 정도로 이해하는 게 안전하다. context, errgroup, supervisor 패턴을 하나로 묶은 설계에 가깝지, Uber나 Google 공식 가이드에 있는 표준은 아니다.

그래서 적용 범위를 명확히 제한한다.

- 도메인 로직은 effect를 "선언"만 하고 직접 실행하지 않는다.
- handler는 infra/runner/orchestration 레이어에만 둔다. 핵심 도메인 로직에는 적용하지 않는다.
- effect가 중첩되기 시작하면 도입을 재검토한다 — 디버깅과 추적이 급격히 어려워진다.

도입 전에 이 세 가지를 먼저 확인한다: errgroup + context로 해결되는가? goroutine 수명과 종료 조건이 한눈에 보이는가? handler 없이도 도메인 로직이 테스트로 검증되는가? 셋 다 아니라면, 그때 제한적으로 고려한다.

## 6. 테스트 설계

- **main/run 분리**: `main()`은 `run()`을 호출만 하고, 서버 초기화·기동 로직은 `run()`에 둔다. `TestMain`에서 서버를 한 번만 띄우면 mock 없이 실제 HTTP 요청으로 테스트할 수 있다.
- **t.Parallel 기본 사용**: 순수 로직 테스트는 기본적으로 `t.Parallel()`을 붙인다. 동시성 버그는 테스트에서 먼저 잡는 게 제일 싸다. 단, DB·파일시스템 등 공유 리소스를 쓰는 테스트는 격리 없이 병렬로 돌리면 안 된다.
- **병렬 테스트는 유니크 데이터로**: `time.Now().UnixNano()` 같은 값으로 데이터 충돌을 피한다.
- **mock보다 real test**: 가능하면 실제로 뜬 서버에 실제 요청을 보내서 검증한다.

### 약한 테스트를 잡는 법 (mutation testing 사고방식)

테스트가 "통과"하는 것과 "올바른 이유로 통과"하는 것은 다르다. 조건문을 뒤집거나(`if x.After(y)` → `if true`) 비교 연산자를 바꿨을 때 테스트가 실패하지 않는다면, 그 테스트는 커버리지 숫자만 채우고 있는 것이다. 자동화 도구(예: go-mutesting)를 CI에 박아 넣기보다는, 보안 관련 코드·핵심 비즈니스 로직처럼 중요한 부분에 한해 코드 리뷰 과정에서 선택적으로 적용하는 게 현실적이다.

실제로 자주 걸리는 약한 테스트 패턴 세 가지:

1. **상태 코드만 검증**: `assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)`만 있고 에러 메시지는 안 본다. 다른 원인으로 같은 상태 코드가 나와도 테스트는 통과해버린다. 상태 코드와 에러 메시지 패턴을 같이 검증해야 한다.
2. **E2E만 있고 unit test가 없음**: E2E 환경에 의존하게 되고 피드백이 느려진다. critical 로직은 unit test로도 반드시 커버한다.
3. **경계값 테스트 부재**: 정확히 100자/101자/200자 같은 경계를 안 건드리면 off-by-one 에러를 못 잡는다. 경계값은 테이블 드리븐 테스트로 명시적으로 다룬다.

새 테스트를 쓸 때 이 체크리스트로 스스로 점검한다.

- 상태 코드뿐 아니라 에러 메시지도 검증하는가?
- 유효/무효 경계값을 모두 테스트하는가?
- critical 로직이라면 unit test로도 커버되는가?
- if 조건을 반전시키면 이 테스트가 실패하는가? (실패 안 하면 약한 테스트)
- 테스트 이름만 보고 뭘 검증하는지 알 수 있는가?

## 원본 자료

- `_archive/gophercon/notes.md` — 세션 정리 원문 (근거·레퍼런스 포함)
- `_archive/gophercon/guides.json` — notes.md와 같은 내용의 구조화 버전
- `_archive/gophercon/test_guides.json` — mutation testing 절차 원문
- `_archive/gophercon/error_mapper_analysis.md` — 4번 항목(에러 응답 방식)의 근거가 된 코드베이스 분석
- `_archive/gophercon/improvements_old_wtm.md` — 예전 wtm 프로젝트 기준 준수도 분석. 지금 이 레포 구조(`cmd/wtm`, `internal/workspace`, `internal/eviction` 등)와 안 맞아서 참고용으로만 남겨뒀다.

# Git Clone Packfile 최적화

## 배경 및 문제 정의

### 증상

`git clone ssh://git@localhost/<owner>/<repo>` 수행 시 8,149개 오브젝트 기준으로 체감 병목이 발생. `GIT_TRACE_PACKET=1`로 확인한 타임라인:

```
18:36:18.559421  version 2 광고 수신
18:36:18.757376  ls-refs 응답 완료
18:36:18.759714  fetch command 전송
18:36:19.224545  packfile 수신 시작   ← 약 465ms 공백
```

ls-refs 완료 후 packfile 첫 바이트까지 **465ms 공백**이 실제 병목 구간.

---

## 원인 분석

### 오해: `git index-pack`은 서버가 아니라 클라이언트에서 실행

trace에 등장하는 아래 로그는 **클라이언트 측** fetch-pack이 서버로부터 받은 packfile을 로컬에서 인덱싱하는 과정이다. 서버가 unpack을 하는 게 아니다.

```
18:36:19.224627 child_start[1] git index-pack --stdin -v --fix-thin ...
```

### 실제 병목: go-git 순수 Go packfile 인코더

`internal/server/tcp_git_server.go`의 `writePackfileWithSideband()` 함수에서 go-git 라이브러리로 packfile을 생성하고 있었다:

```go
enc := packfile.NewEncoder(sbw, st, useRefDelta)
_, encodeErr = enc.Encode(hashes, packWindow)   // ← 여기가 병목
```

**왜 느린가:**

| 항목 | go-git 인코더 | native git pack-objects |
|------|--------------|------------------------|
| 구현 언어 | 순수 Go | C |
| 속도 | native의 1/5 ~ 1/10 | 기준 |
| Pack 재활용 | 불가 (항상 재인코딩) | 기존 .pack 파일 재활용 |
| Delta 검색 | `packWindow=0`으로 꺼도 오브젝트 전체 읽기 | 최적화된 seek 패턴 |
| 8,149 objects 처리 | window=10 시 81,490 seek → ~30초 | 수초 내 완료 |

초기 구현에서 Docker volume 환경에서의 랜덤 seek 문제로 `packWindow=0`(delta 비활성화)으로 우회했지만, 여전히 순수 Go로 오브젝트를 하나씩 읽어 재인코딩해야 했다.

---

## 참고: GitHub / GitLab의 접근 방식

### GitLab (Gitaly)

```
git pack-objects → git upload-pack → Gitaly(Go) → gitlab-shell → 클라이언트
```

- Gitaly는 `git upload-pack`을 subprocess로 띄우고 stdin/stdout을 그대로 파이프
- Go 레이어의 역할: **프로토콜 재구현이 아니라 인증/접근제어/이벤트 중개**
- Pack 생성 자체는 완전히 native git에 위임

### GitHub

- **Pack bitmap index**: 오브젝트 도달 가능성을 비트맵으로 O(1) 조회
- **Pack reuse**: 기존 `.pack` 파일을 재인코딩 없이 직접 스트리밍

### 공통 원칙

> Git 서버의 역할은 git 프로토콜을 재구현하는 것이 아니라, **인증·정책·관측성**을 담당하고 실제 pack 생성은 native git binary에 위임하는 것이다.

---

## 구현: `writePackfileViaGitPackObjects`

### 위치

`internal/server/tcp_git_server.go`

### 동작 방식

```
클라이언트 want/have
        ↓
  (stdin rev-list 형식)
        ↓
git pack-objects --stdout --revs
        ↓
  raw PACK binary
        ↓
sidebandWriter (ch1, pkt-line 프레임)
        ↓
  TCP 소켓 → 클라이언트
```

### 코드

```go
func (s *DuplexTCPServer) writePackfileViaGitPackObjects(
    ctx context.Context,
    conn *connection.ProgressAwareConnection,
    repoPath string,
    fetchReq *protocol.FetchReq,
) error {
    args := []string{"-C", repoPath, "pack-objects", "--stdout", "--revs"}
    if fetchReq.Deepen > 0 {
        args = append(args, "--depth="+strconv.Itoa(fetchReq.Deepen))
    }
    cmd := exec.CommandContext(ctx, "git", args...)
    cmd.Dir = repoPath

    var stdinBuf bytes.Buffer
    for _, w := range fetchReq.Wants {
        stdinBuf.WriteString(w.String())
        stdinBuf.WriteByte('\n')
    }
    for _, h := range fetchReq.Haves {
        stdinBuf.WriteString("^")
        stdinBuf.WriteString(h.String())
        stdinBuf.WriteByte('\n')
    }
    cmd.Stdin = &stdinBuf

    sbw := newSidebandWriter(conn.GetWriter(), 0x01)
    cmd.Stdout = sbw
    cmd.Stderr = io.Discard

    if err := cmd.Start(); err != nil {
        return err
    }
    if err := cmd.Wait(); err != nil {
        return err
    }
    if err := conn.Flush(); err != nil {
        return fmt.Errorf("flush after pack-objects: %w", err)
    }
    return nil
}
```

### 호출 흐름

```go
// tcp_git_server.go:557
encErr := s.writePackfileViaGitPackObjects(packCtx, conn, repoPath, fetchReq)
if encErr != nil {
    encErr = s.writePackfileWithSideband(conn, underlyingRepo.Storer, objectHashes, fetchReq)
}
```

primary: `git pack-objects` subprocess → 실패 시 fallback: go-git 인코더

---

## 현재 구현 검토

### 올바르게 동작하는 것 ✓

| 항목 | 근거 |
|------|------|
| `--revs` + stdin으로 want/have 전달 | `git rev-list` 형식 정석 |
| `cmd.Stdout = sbw` | subprocess 출력이 sideband ch1으로 직접 스트리밍 |
| `sbw.Flush()` 미호출 | 호출자(line 586)가 `0000` section 종료자 전송 — 중복 방지 |
| `exec.CommandContext` | 15분 timeout context 연동, 취소 시 subprocess kill |
| `cmd.Wait()` | subprocess 완료 후 bufio 버퍼가 모두 write됨을 보장 |
| fallback 구조 | pack-objects 실패 시 go-git 인코더로 안전하게 폴백 |

### 미흡한 점

#### 1. `--thin` 플래그 미전달 (팩 크기 영향)

`fetchReq.ThinPack == true`일 때 클라이언트는 thin-pack을 지원한다고 알린 것이다.
`--thin`이 없으면 pack-objects가 클라이언트 have 오브젝트를 delta base로 활용하지 못해 팩이 더 커진다.

```go
// 현재 누락
if fetchReq.ThinPack {
    args = append(args, "--thin")
}
```

#### 2. `--ofs-delta` 플래그 미전달 (압축 효율 영향)

`fetchReq.OfsDelta == true`일 때 offset-delta를 사용해야 한다.
fallback인 `writePackfileWithSideband`는 `useRefDelta := !fetchReq.OfsDelta`로 처리하는데, 이 함수에서는 누락되어 있다.

```go
// 현재 누락
if fetchReq.OfsDelta {
    args = append(args, "--ofs-delta")
}
```

#### 3. `-C repoPath`와 `cmd.Dir` 중복

```go
args := []string{"-C", repoPath, ...}   // git 레벨 cd
cmd.Dir = repoPath                        // OS 레벨 cd
```

둘 다 같은 효과. 동작에는 문제없지만 하나는 제거해야 한다.

#### 4. `cmd.Stderr = io.Discard`로 인한 디버깅 어려움

pack-objects가 실패해도 오류 메시지가 사라진다. `cmd.Wait()`은 `exit status 128` 수준만 반환하므로 원인 파악이 불가능하다.

```go
// 개선 방향
var stderrBuf bytes.Buffer
cmd.Stderr = &stderrBuf
if err := cmd.Wait(); err != nil {
    return fmt.Errorf("pack-objects: %w; stderr: %s", err, stderrBuf.String())
}
```

#### 5. `BuildFetchObjectList` 이중 열거 (성능 낭비)

pack-objects 성공 경로에서도 line 509에서 go-git으로 오브젝트 목록을 먼저 만든다.

```go
fetchResult, err := protocol.BuildFetchObjectList(...)  // go-git 열거 (1회)
objectHashes := fetchResult.Objects

encErr := s.writePackfileViaGitPackObjects(...)          // native git 열거 (2회)
```

`objectHashes`는 성공 경로에서는 progress 메시지의 `totalObjects` 카운트와 fallback에만 쓰인다. pack-objects가 성공하면 go-git 열거는 낭비다.

---

## 남은 작업

| 우선순위 | 항목 | 영향 |
|---------|------|------|
| 높음 | `--thin` 플래그 추가 | clone 시 전송 팩 크기 감소 |
| 높음 | `--ofs-delta` 플래그 추가 | delta 압축 효율 개선 |
| 중간 | stderr 캡처 | 실패 원인 파악 가능 |
| 중간 | `-C` 또는 `cmd.Dir` 중 하나 제거 | 코드 정리 |
| 낮음 | `BuildFetchObjectList` 호출 제거 (성공 경로) | go-git 열거 1회 절약 |
| 낮음 | HTTP/SSH 핸들러에 동일 패턴 적용 | `upload_pack.go`, `ssh_protocol.go`의 go-git 인코더 교체 |

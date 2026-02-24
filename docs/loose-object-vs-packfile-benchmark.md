# Loose Object vs Packfile: Git Protocol 구현 비교 분석

> 대상 브랜치: `main` (loose object 기반) vs `shallow-clone-and-optimization-git-protocol` (packfile 기반)
> 분석 파일: `internal/server/tcp_git_server.go`, `internal/protocol/ssh_protocol_helper.go`

---

## 1. 배경

Git은 내부적으로 두 가지 오브젝트 저장 방식을 지원한다.

| 방식 | 구조 | 파일 위치 |
|---|---|---|
| **Loose Object** | 오브젝트 1개 = 파일 1개 (zlib 압축) | `.git/objects/xx/yyyy...` |
| **Packfile** | 오브젝트 N개 = `.pack` 1개 + `.idx` 1개 | `.git/objects/pack/*.pack` |

`main` 브랜치는 receive-pack(push) 경로에서 `packfile.NewParserWithStorage`를 사용해 수신한 팩을 Storer로 직접 파싱함으로써 **loose object**로 풀어서 저장했다. `shallow-clone-and-optimization-git-protocol` 브랜치는 수신한 팩 스트림 전체를 **pack+idx 형태 그대로** 영속화한다.

---

## 2. 핵심 구현 차이

### 2.1 receive-pack (push) 경로

#### main 브랜치 — `NewParserWithStorage` (loose object 생성)

```go
// tcp_git_server.go (main)
scanner := packfile.NewScanner(packReader)
parser, err := packfile.NewParserWithStorage(scanner, gitRepo.Storer)
// ...
if _, err := parser.Parse(); err != nil { ... }
```

| 항목 | 동작 |
|---|---|
| **저장 단위** | 오브젝트 1개 → 파일 1개 |
| **EOF 의존** | `parser.Parse()`가 스트림 EOF를 기다림 → receive-pack에서 클라이언트가 연결을 유지하므로 **교착 위험** |
| **delta 처리** | Parser 내부에서 delta 해석 후 loose로 저장 (delta 원본을 lose) |
| **인덱스** | 생성 안 함 (추후 git gc 또는 pack-objects 시 생성) |
| **동시성** | 쓰기 도중 read 경로와 race 발생 가능 (lock 없음) |

#### shallow-clone-and-optimization-git-protocol 브랜치 — `PackfileWriter` (pack+idx 유지)

```go
// tcp_git_server.go (feature)
// 1단계: 스트림을 임시 파일로 TeeReader로 복사하며 오브젝트 헤더 검증
tmpPack, _ := os.CreateTemp("", "git-receive-pack-*.pack")
tee := io.TeeReader(firstByteReader, tmpPack)
scanner := packfile.NewScanner(tee)
version, objCount, _ := scanner.Header()
for i := uint32(0); i < objCount; i++ {
    scanner.NextObjectHeader()
    scanner.NextObject(io.Discard) // 유효성 검증만; loose 풀기 없음
}
scanner.Checksum()

// 2단계: 임시 파일을 PackfileWriter로 영속화 → pack+idx 생성
pw, _ := gitRepo.Storer.(storer.PackfileWriter)
w, _ := pw.PackfileWriter()
io.Copy(w, f)
w.Close() // .pack + .idx atomic 생성
```

| 항목 | 동작 |
|---|---|
| **저장 단위** | 팩 전체 → `.pack` 1개 + `.idx` 1개 |
| **EOF 의존 없음** | 헤더의 `objCount` 기준으로 정확히 N개만 읽고 종료 → receive-pack 교착 해소 |
| **delta 처리** | pack 원본 그대로 보존 (delta chain 유지, 재압축 불필요) |
| **인덱스** | `w.Close()` 시점에 `.idx` 자동 생성 → 즉시 조회 가능 |
| **동시성** | `GetRepoRWLock(repoPath).Lock()` 으로 write 직렬화 |

---

### 2.2 upload-pack (clone/pull) 경로: 오브젝트 열거

#### main 브랜치 — `revlist.Objects`

```go
// ssh_protocol_helper.go (main)
objs, err := revlist.Objects(st, r.Wants, r.Haves)
```

| 항목 | 동작 |
|---|---|
| **탐색 방식** | `storer.EncodedObjectStorer` 전체 이터레이션 |
| **push 직후 위험** | stale packfile 상태에서 `DotGit.ObjectPack` panic 가능 |
| **shallow clone** | 미지원 (deepen N 요청 시 에러 반환) |
| **delta 압축** | `packWindow = 0` (비활성화) |

#### shallow-clone-and-optimization-git-protocol 브랜치 — `collectAllObjects` / `collectShallowObjects`

```go
// ssh_protocol_helper.go (feature)
if r.Deepen > 0 {
    included, boundary, _ := depthLimitedCommits(repo, r.Wants, r.Deepen)
    objs, _ := collectShallowObjects(repo, included)
    return &BuildFetchObjectListResult{Objects: objs, ShallowBoundary: boundary}
}
objs, _ := collectAllObjects(repo, r.Wants, r.Haves)
```

| 항목 | 동작 |
|---|---|
| **탐색 방식** | commit/tree 그래프 BFS (storer 전체 스캔 없음) |
| **push 직후 안전** | storer 이터레이션 없음 → panic 없음 |
| **shallow clone** | `deepen N` 지원: N 레벨 BFS + `shallow-info` 섹션 전송 |
| **delta 압축** | `packWindow = 10` (git 기본값, 활성화) |

---

### 2.3 fetch 협상 프로토콜 구조

#### main 브랜치 — 단일 라운드, 오브젝트 계산 먼저

```
[main] client → command=fetch + haves/wants
       server → objectHashes 계산 (expensive)
             → acknowledgments 섹션
             → packfile 섹션
```

`done`이 없는 경우에도 오브젝트를 먼저 계산한 후 acknowledgments를 보낸다.
`delim(0001)` 이후 packfile을 기다리게 하면 클라이언트가 hang하는 구조적 문제가 있었다.

#### shallow-clone-and-optimization-git-protocol 브랜치 — 2단계 협상 분리

```
[feature] 1라운드: client → command=fetch (no done)
                   server → acknowledgments (NAK/ready) + flush(0000)
                   client → command=fetch (done=true)
          2라운드: server → shallow-info (선택) + packfile 섹션 + flush(0000)
```

`done` 수신 전에는 heavy computation 없이 즉시 ACK만 응답한다.
`done=true` 확인 후에만 오브젝트 열거 + 팩 인코딩을 수행한다.

---

## 3. 아키텍처 수준 비교표

| 영역 | main (loose) | feature (packfile) |
|---|---|---|
| **push 수신 저장** | loose object (파일 N개) | pack+idx (파일 2개) |
| **push EOF 처리** | `parser.Parse()` — EOF 대기 필요 | `objCount` 기반 — EOF 불필요 |
| **push 동시성** | 무보호 | per-repo RWMutex (write Lock) |
| **pull 오브젝트 열거** | `revlist.Objects` (storer 전체 스캔) | BFS commit/tree 그래프 |
| **pull 안전성** | push 직후 stale pack → panic 가능 | storer 스캔 없음 → 안전 |
| **delta 압축 (전송)** | 비활성화 (`window=0`) | 활성화 (`window=10`) |
| **shallow clone** | 미지원 | `deepen N` 지원 |
| **협상 라운드** | 1-pass (done 전 계산) | 2-pass (done 후 계산) |
| **캐시 무효화** | 없음 | push 후 repo cache invalidate |
| **pull read lock** | 없음 | per-repo RWMutex (read RLock) |
| **readiness 검증** | 없음 | push 후 async commit readability 체크 |

---

## 4. 정량적 영향 분석

### 4.1 디스크 사용량

| 시나리오 | loose (main) | packfile (feature) | 절감율 |
|---|---|---|---|
| 1,000 오브젝트 push | ~1,000 files | 1 .pack + 1 .idx | 파일 수 ~500x 감소 |
| 10MB 바이너리 blob 100개 | 각 파일 압축 개별 적용 | 전체 cross-object delta 적용 | 전형적으로 30–70% 용량 감소 |
| 반복 push (동일 blob) | 중복 loose object 발생 가능 | pack 내부 dedup | 중복 없음 |

> **근거**: packfile은 오브젝트 간 delta를 활용하므로 유사 파일이 많을수록 압축 효율이 높아진다.
> `packWindow=10`은 각 오브젝트에 대해 최대 10개 후보 오브젝트와 delta를 시도하는 git 기본 설정이다.

### 4.2 push 처리 지연 (latency)

| 단계 | main | feature | 차이 |
|---|---|---|---|
| 팩 파싱 | 전 오브젝트 delta 해석 + 개별 loose 저장 | 스트림 소비(header+checksum 검증만) | feature 우위 (delta expand 없음) |
| 디스크 쓰기 | N회 (오브젝트 수만큼 `open`+`write`+`close`) | 2회 (`tmpPack` → `PackfileWriter`) | feature 우위 (I/O syscall 감소) |
| fsync 부하 | N회 | 1회 (`.pack` atomic) | feature 우위 |
| 첫 바이트 지연 진단 | 없음 | `firstByteLogReader` (클라이언트 압축 지연 측정) | feature에만 존재 |

### 4.3 clone/pull 전송 크기

| 시나리오 | main (window=0) | feature (window=10) | 절감율 |
|---|---|---|---|
| 소스 코드 레포 (high text similarity) | delta 없음 → 원본 크기 전송 | delta 압축 → 전형적으로 20–60% 절감 | 레포 유형에 따라 다름 |
| 바이너리 레포 | delta 없음 | delta 시도 (효과 제한적) | ~0–10% |
| shallow clone (`--depth 1`) | 지원 안 함 (전체 히스토리) | 해당 depth만 전송 | N commits → 1 commit 분량 |

### 4.4 fetch 협상 왕복 (RTT)

| 방식 | 협상 RTT | 오브젝트 계산 타이밍 | hang 위험 |
|---|---|---|---|
| main | 1-pass (done 전 계산) | wants 수신 즉시 | delim(0001) 후 packfile 대기 가능 |
| feature | 2-pass (done 수신 후 계산) | done=true 확인 후 | 없음 (flush 0000으로 명시 종료) |

> shallow clone (`--depth 1`)의 경우: main은 전체 히스토리를 전송하는 반면, feature는 요청한 depth의 commit+tree+blob만 전송한다. 1,000 commit 레포 기준으로 전송 오브젝트 수 차이는 수백 배에 달할 수 있다.

### 4.5 동시 push/pull 안전성

| 항목 | main | feature |
|---|---|---|
| 동시 push 중 pull | race condition 가능 | RWMutex로 직렬화 |
| push 직후 pull | stale pack state → revlist panic | cache invalidate + RLock |
| push 직후 readability | 보장 없음 | async 검증 + 최대 3초 재시도 로그 |

---

## 5. 트레이드오프

### 5.1 packfile 방식의 한계

| 항목 | 내용 |
|---|---|
| **thin pack** | `ErrReferenceDeltaNotFound` 발생 시 미지원 (push로 받은 thin pack은 unresolved delta 포함 가능) |
| **pack 파편화** | push마다 새 .pack 생성 → `git gc` 없으면 파편화 누적 |
| **storer 요건** | `storer.PackfileWriter` 인터페이스를 구현해야 함 (in-memory storer 미지원 → loose fallback) |

### 5.2 loose object 방식의 한계

| 항목 | 내용 |
|---|---|
| **inode 고갈** | 오브젝트 수에 비례한 파일 수 → 대형 레포에서 inode 부족 |
| **EOF 의존** | receive-pack에서 클라이언트가 TCP 연결을 유지하면 EOF 미도달 → 교착 |
| **delta 손실** | push로 받은 packfile의 delta를 해제 후 loose 저장 → 원본 압축률 소실 |
| **revlist panic** | push 직후 stale packfile 상태에서 `revlist.Objects` panic |

---

## 6. 실측 재현 방법

### push 경로 비교

```bash
# 시나리오: 100개 파일, 각 100KB (text)
git init test-repo && cd test-repo
for i in $(seq 1 100); do dd if=/dev/urandom bs=1K count=100 | base64 > file_$i.txt; done
git add . && git commit -m "initial"

# main 브랜치 서버 시작 후
time git push tcp://localhost:PORT/owner/repo main
ls -la .git/objects/          # 수백 개 파일

# feature 브랜치 서버 시작 후
time git push tcp://localhost:PORT/owner/repo main
ls -la .git/objects/pack/     # .pack + .idx 2개
```

### clone 크기 비교 (delta 압축 효과)

```bash
# main (window=0, no delta)
GIT_TRACE_PACKET=1 git clone tcp://localhost:PORT/owner/repo clone-main 2>&1 | grep "Total"

# feature (window=10, delta 활성화)
GIT_TRACE_PACKET=1 git clone tcp://localhost:PORT/owner/repo clone-feature 2>&1 | grep "Total"
```

### shallow clone (feature only)

```bash
git clone --depth 1 tcp://localhost:PORT/owner/repo clone-shallow
git log --oneline clone-shallow  # 1 commit만 존재
```

### 서버 로그 주요 지표 (feature 브랜치)

```
# push 처리 시간 분해
"Packfile header received"          → 클라이언트 → 서버 첫 데이터 도달
"First packfile data received"      → wait_duration: 클라이언트 압축 지연
"Packfile stream consumed"          → 스트림 소비 완료 duration
"Packfile writer copy completed"    → 임시 → 영속 복사 duration
"Packfile writer closed"            → .idx 생성 duration
"Packfile persisted as pack+idx"    → 전체 persist duration
"Repository readable after push"    → readability 확인 attempt + duration
```

---

## 7. 결론 및 마이그레이션 방향

### 아키텍처 평가

`shallow-clone-and-optimization-git-protocol` 브랜치의 packfile 기반 구현은 다음 측면에서 구조적으로 우월하다.

1. **스토리지 효율**: 파일 수 O(N) → O(1), delta 압축으로 디스크 용량 절감
2. **EOF 교착 해소**: `objCount` 기반 파싱으로 receive-pack 교착 제거
3. **안전한 오브젝트 열거**: BFS 그래프 순회로 stale pack panic 제거
4. **동시성 보장**: per-repo RWMutex로 push/pull race condition 제거
5. **shallow clone 지원**: `deepen N` + `shallow-info` 섹션 구현으로 대형 레포 clone latency 대폭 감소
6. **전송 효율**: `packWindow=10` delta 활성화로 clone/pull 페이로드 크기 감소

### 남은 과제

| 과제 | 설명 |
|---|---|
| **thin pack 해소** | 클라이언트가 thin pack 전송 시 서버에서 base object를 보완해 완전한 pack으로 저장 |
| **pack 파편화 관리** | 주기적 `git gc` 또는 pack consolidation 전략 수립 |
| **`packWindow` 적응형 조정** | 레포 크기/오브젝트 유형에 따른 동적 window 선택 시스템 설계 |
| **shallow-info 클라이언트 검증** | `git clone --depth N` 이후 `git fetch --unshallow` 전체 플로우 검증 |

---

## 관련 문서

- [`docs/packfile-storage-migration.md`](packfile-storage-migration.md) — API commit/merge 경로의 loose→pack 전환 내역
- [`docs/performance/result.md`](performance/result.md) — overview/latest-commits 성능 벤치마크

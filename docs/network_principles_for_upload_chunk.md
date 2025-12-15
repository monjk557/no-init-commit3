# Network Principles for Upload Pack Optimization

> Git upload-pack 프로토콜의 대용량 레포지토리 처리를 위한 네트워킹 법칙 적용 가이드

## 목차
1. [현재 구현의 문제점](#1-현재-구현의-문제점)
2. [적용 가능한 네트워킹 법칙](#2-적용-가능한-네트워킹-법칙)
3. [구체적인 개선 방안](#3-구체적인-개선-방안)
4. [대용량 파일 처리 개선 효과](#4-대용량-파일-처리-개선-효과)
5. [구현 로드맵](#5-구현-로드맵)

---

## 1. 현재 구현의 문제점

### 1.1 메모리 병목

현재 `internal/protocol/upload_pack.go`의 구현:

```go
// Line 524-526
objectHashes, err := revlist.Objects(storer, wantHashes, haveHashes)
// 문제: 대용량 레포에서 objectHashes가 수백만 개가 될 수 있음
// 예: 100만 객체 × 20바이트(해시) = 20MB+ 메모리
```

**문제점:**
- 모든 객체 해시를 메모리에 한번에 로드
- 클라이언트가 첫 바이트를 받기까지 전체 계산 완료 필요
- 대용량 레포에서 OOM(Out of Memory) 위험

### 1.2 고정 청크 사이즈

```go
// Line 58
chunkSize: 65515,  // 고정 64KB
```

**문제점:**
- 네트워크 조건(RTT, 대역폭)을 고려하지 않음
- 고지연 네트워크에서 파이프라인 효율 저하
- 대용량 파일 전송 시 최적화되지 않음

### 1.3 직렬 처리 구조

```go
// 현재 흐름 (직렬)
1. revlist.Objects() 완료 대기 (전체 객체 탐색)
2. enc.Encode() 완료 대기 (전체 packfile 생성)
3. 네트워크 전송

// 문제: 각 단계가 완료되어야 다음 단계 시작
```

### 1.4 Backpressure 부재

```go
// Line 110-120
if sb.flusher != nil {
    sb.flusher.Flush()  // 무조건 flush, 클라이언트 상태 무시
}
```

**문제점:**
- 느린 클라이언트가 빠른 서버를 따라잡지 못함
- 서버 메모리에 데이터 누적 → OOM 가능

---

## 2. 적용 가능한 네트워킹 법칙

### 2.1 Bandwidth-Delay Product (BDP)

```
공식: BDP = Bandwidth × RTT

예시:
- 100 Mbps, 50ms RTT → BDP = 625 KB
- 1 Gbps, 10ms RTT → BDP = 1.25 MB
- 10 Mbps, 200ms RTT → BDP = 250 KB

의미: 네트워크 "파이프"에 동시에 존재할 수 있는 데이터량
```

**Git 전송에 적용:**
- 청크 크기를 BDP에 맞추면 파이프를 최대한 활용
- 고정 64KB는 대부분의 경우 BDP보다 작음 → 대역폭 낭비

### 2.2 Little's Law (리틀의 법칙)

```
공식: L = λ × W

L = 시스템 내 평균 항목 수 (큐 길이)
λ = 처리율 (throughput)
W = 평균 체류 시간 (latency)

재해석: λ = L / W
→ 처리율을 높이려면: L을 늘리거나 W를 줄여야 함
```

**Git 전송에 적용:**
- 현재: W가 매우 큼 (전체 객체 계산 후 전송)
- 개선: 스트리밍으로 W를 줄이고, 파이프라이닝으로 L을 늘림

### 2.3 End-to-End Principle (종단간 원칙)

```
"기능은 가능한 한 끝점(endpoint)에서 구현하라"

중간 노드의 복잡성 최소화 → 유연성, 안정성 향상
```

**Git 전송에 적용:**
- 현재: 서버가 모든 delta 계산 담당
- 개선: thin-pack으로 클라이언트가 delta 해제
- 효과: 서버 부하 분산, 클라이언트가 이미 가진 데이터 활용

### 2.4 Backpressure (역압)

```
"빠른 생산자가 느린 소비자를 압도하면 안 된다"

TCP Flow Control의 핵심 원리
Reactive Streams, Go channels의 기본 원칙
```

**Git 전송에 적용:**
- 클라이언트 수신 속도에 맞춰 서버 전송 속도 조절
- 버퍼 오버플로우 방지
- 메모리 사용량 예측 가능

### 2.5 Amdahl's Law (암달의 법칙)

```
공식: Speedup = 1 / ((1-P) + P/N)

P = 병렬화 가능한 비율
N = 프로세서/스레드 수

예: 80% 병렬화 가능, 4코어
Speedup = 1 / (0.2 + 0.8/4) = 1 / 0.4 = 2.5x (4x가 아님!)
```

**Git 전송에 적용:**
- 직렬 부분(객체 탐색)이 병목
- 파이프라인 병렬화로 직렬 부분 최소화

### 2.6 Zero-Copy Principle

```
일반 경로 (4번 복사):
Disk → Kernel Buffer → User Buffer → Socket Buffer → NIC

Zero-copy 경로:
Disk → Kernel Buffer → NIC (sendfile syscall)
```

**Git 전송에 적용:**
- 현재: Disk → Object → Packfile → Sideband → HTTP (4번 복사)
- 개선: io.Pipe로 중간 버퍼 최소화

---

## 3. 구체적인 개선 방안

### 3.1 BDP-aware Dynamic Chunking

```go
// 개선된 sidebandWriter
type AdaptiveSidebandWriter struct {
    w              io.Writer
    flusher        http.Flusher
    channel        byte
    
    // BDP 기반 동적 청크
    chunkSize      int
    minChunkSize   int  // 8KB
    maxChunkSize   int  // 64KB
    
    // RTT 추정
    lastWriteTime  time.Time
    lastAckTime    time.Time
    estimatedRTT   time.Duration
    bandwidth      float64  // bytes/sec
}

func (sb *AdaptiveSidebandWriter) calculateOptimalChunkSize() int {
    if sb.estimatedRTT == 0 {
        return 32768  // 기본값 32KB
    }
    
    // BDP = Bandwidth × RTT
    bdp := sb.bandwidth * sb.estimatedRTT.Seconds()
    
    // 청크 크기 = BDP의 1/4 ~ 1/2 (여러 청크가 파이프에 있도록)
    optimal := int(bdp / 2)
    
    // 범위 제한
    if optimal < sb.minChunkSize {
        return sb.minChunkSize
    }
    if optimal > sb.maxChunkSize {
        return sb.maxChunkSize
    }
    return optimal
}

func (sb *AdaptiveSidebandWriter) updateRTTEstimate(ackReceived bool) {
    if ackReceived {
        rtt := time.Since(sb.lastWriteTime)
        // Exponential moving average
        if sb.estimatedRTT == 0 {
            sb.estimatedRTT = rtt
        } else {
            sb.estimatedRTT = time.Duration(
                0.875*float64(sb.estimatedRTT) + 0.125*float64(rtt))
        }
    }
}
```

**대용량 파일 효과:**
- 고대역폭 환경에서 청크 크기 자동 증가 → 오버헤드 감소
- 저대역폭/고지연 환경에서 청크 크기 감소 → 응답성 유지

### 3.2 Streaming Pipeline (Little's Law 적용)

```go
// 3단계 파이프라인 구조
type StreamingPackfileGenerator struct {
    repo    *gogit.Repository
    storer  storage.Storer
    
    // Pipeline stages
    objectChan  chan plumbing.Hash     // Stage 1 → 2
    encodedChan chan *EncodedObject    // Stage 2 → 3
    
    // Control
    ctx    context.Context
    cancel context.CancelFunc
    wg     sync.WaitGroup
}

// Stage 1: Object Discovery (Producer)
func (g *StreamingPackfileGenerator) discoverObjects(
    wants, haves []plumbing.Hash,
) {
    defer close(g.objectChan)
    
    // BFS로 객체 그래프 탐색
    visited := make(map[plumbing.Hash]bool)
    queue := make([]plumbing.Hash, 0, len(wants))
    
    // haves를 visited에 미리 추가 (클라이언트가 이미 가진 것)
    for _, h := range haves {
        visited[h] = true
    }
    
    // wants부터 시작
    queue = append(queue, wants...)
    
    for len(queue) > 0 {
        select {
        case <-g.ctx.Done():
            return
        default:
        }
        
        hash := queue[0]
        queue = queue[1:]
        
        if visited[hash] {
            continue
        }
        visited[hash] = true
        
        // 객체를 다음 단계로 전달 (스트리밍!)
        g.objectChan <- hash
        
        // 객체의 자식들을 큐에 추가
        obj, err := g.repo.Storer.EncodedObject(plumbing.AnyObject, hash)
        if err != nil {
            continue
        }
        
        children := g.getObjectChildren(obj)
        queue = append(queue, children...)
    }
}

// Stage 2: Delta Encoding (Transformer)
func (g *StreamingPackfileGenerator) encodeObjects() {
    defer close(g.encodedChan)
    
    for hash := range g.objectChan {
        select {
        case <-g.ctx.Done():
            return
        default:
        }
        
        obj, err := g.storer.EncodedObject(plumbing.AnyObject, hash)
        if err != nil {
            continue
        }
        
        encoded := &EncodedObject{
            Hash:   hash,
            Type:   obj.Type(),
            Size:   obj.Size(),
            Reader: obj.Reader(),
        }
        
        g.encodedChan <- encoded
    }
}

// Stage 3: Streaming Output (Consumer)
func (g *StreamingPackfileGenerator) streamToWriter(w io.Writer) error {
    // Packfile 헤더 먼저 전송
    header := packfile.NewHeader(0)  // 객체 수는 나중에
    if _, err := w.Write(header); err != nil {
        return err
    }
    
    count := 0
    for encoded := range g.encodedChan {
        // 객체를 바로 writer에 전송
        if err := g.writeEncodedObject(w, encoded); err != nil {
            return err
        }
        count++
    }
    
    // Packfile 트레일러 전송
    return g.writeTrailer(w, count)
}
```

**대용량 파일 효과:**
- 첫 바이트 전송 시간: 분 단위 → 초 단위
- 메모리 사용: O(n) → O(buffer_size) = 상수
- 진행률 표시 가능 (스트리밍이므로)

### 3.3 Backpressure-aware Writer

```go
type BackpressureWriter struct {
    w              io.Writer
    flusher        http.Flusher
    
    // Flow control
    inflightBytes  int64
    maxInflight    int64      // BDP 기반으로 설정
    windowSize     int64      // 슬라이딩 윈도우
    
    // Congestion detection
    writeLatencies []time.Duration
    congested      bool
    
    mu             sync.Mutex
    cond           *sync.Cond
}

func NewBackpressureWriter(w io.Writer, maxInflight int64) *BackpressureWriter {
    bw := &BackpressureWriter{
        w:              w,
        maxInflight:    maxInflight,
        windowSize:     maxInflight,
        writeLatencies: make([]time.Duration, 0, 100),
    }
    bw.cond = sync.NewCond(&bw.mu)
    
    if f, ok := w.(http.Flusher); ok {
        bw.flusher = f
    }
    
    return bw
}

func (bw *BackpressureWriter) Write(p []byte) (int, error) {
    bw.mu.Lock()
    
    // Backpressure: inflight가 너무 많으면 대기
    for bw.inflightBytes >= bw.windowSize {
        // 혼잡 감지
        bw.congested = true
        bw.cond.Wait()
    }
    
    bw.mu.Unlock()
    
    // 실제 쓰기
    start := time.Now()
    n, err := bw.w.Write(p)
    latency := time.Since(start)
    
    bw.mu.Lock()
    defer bw.mu.Unlock()
    
    if err == nil {
        bw.inflightBytes += int64(n)
        bw.recordLatency(latency)
        bw.adjustWindow()
    }
    
    return n, err
}

func (bw *BackpressureWriter) recordLatency(latency time.Duration) {
    bw.writeLatencies = append(bw.writeLatencies, latency)
    if len(bw.writeLatencies) > 100 {
        bw.writeLatencies = bw.writeLatencies[1:]
    }
}

func (bw *BackpressureWriter) adjustWindow() {
    // AIMD (Additive Increase, Multiplicative Decrease)
    // TCP Congestion Control과 유사
    
    avgLatency := bw.avgLatency()
    
    if avgLatency > 100*time.Millisecond {
        // 혼잡 감지: 윈도우 절반으로
        bw.windowSize = max(bw.windowSize/2, 8192)
        bw.congested = true
    } else if !bw.congested {
        // 정상: 윈도우 점진적 증가
        bw.windowSize = min(bw.windowSize+1024, bw.maxInflight)
    }
}

func (bw *BackpressureWriter) avgLatency() time.Duration {
    if len(bw.writeLatencies) == 0 {
        return 0
    }
    var sum time.Duration
    for _, l := range bw.writeLatencies {
        sum += l
    }
    return sum / time.Duration(len(bw.writeLatencies))
}

// Acknowledge: 클라이언트가 데이터를 받았음을 확인
func (bw *BackpressureWriter) Acknowledge(bytes int64) {
    bw.mu.Lock()
    defer bw.mu.Unlock()
    
    bw.inflightBytes -= bytes
    if bw.inflightBytes < 0 {
        bw.inflightBytes = 0
    }
    
    bw.congested = false
    bw.cond.Signal()  // 대기 중인 Write 깨우기
}
```

**대용량 파일 효과:**
- OOM 방지: 메모리 사용량 예측 가능
- 느린 클라이언트 지원: 타임아웃 없이 안정적 전송
- 네트워크 혼잡 시 자동 속도 조절

### 3.4 Pipeline Parallelism (Amdahl's Law 적용)

```go
// 기존 직렬 처리 vs 파이프라인 병렬 처리

/* 기존 (직렬):
   
   Time: |----Object Discovery----|----Delta Encoding----|----Compression----|----Network----|
         |<------- 10초 -------->||<------ 20초 ------>||<----- 5초 ---->||<--- 10초 --->|
         
   Total: 45초
   첫 바이트: 35초 후
*/

/* 파이프라인 (병렬):
   
   Time: |----Object Discovery---------------------------------->|
              |----Delta Encoding--------------------------------->|
                   |----Compression-------------------------------->|
                        |----Network-------------------------------->|
         
   Total: ~18초 (가장 느린 단계 + 오버헤드)
   첫 바이트: ~3초 후 (첫 객체가 파이프라인 통과 시간)
*/

type ParallelPackfileGenerator struct {
    // 단계별 워커 수
    discoveryWorkers  int  // 보통 1 (그래프 탐색은 직렬)
    encodingWorkers   int  // CPU 코어 수
    compressionWorkers int // CPU 코어 수 / 2
    
    // 단계별 버퍼 크기
    objectBuffer      int  // 1000
    encodedBuffer     int  // 100
    compressedBuffer  int  // 50
}

func (g *ParallelPackfileGenerator) Generate(
    ctx context.Context,
    wants, haves []plumbing.Hash,
    output io.Writer,
) error {
    // 파이프라인 채널
    objects := make(chan plumbing.Hash, g.objectBuffer)
    encoded := make(chan *EncodedObject, g.encodedBuffer)
    compressed := make(chan *CompressedChunk, g.compressedBuffer)
    
    // 에러 수집
    errChan := make(chan error, 4)
    
    // Stage 1: Discovery (1 goroutine)
    go func() {
        errChan <- g.discoverObjects(ctx, wants, haves, objects)
    }()
    
    // Stage 2: Encoding (N goroutines)
    var encodeWg sync.WaitGroup
    for i := 0; i < g.encodingWorkers; i++ {
        encodeWg.Add(1)
        go func() {
            defer encodeWg.Done()
            g.encodeWorker(ctx, objects, encoded)
        }()
    }
    go func() {
        encodeWg.Wait()
        close(encoded)
    }()
    
    // Stage 3: Compression (M goroutines)
    var compressWg sync.WaitGroup
    for i := 0; i < g.compressionWorkers; i++ {
        compressWg.Add(1)
        go func() {
            defer compressWg.Done()
            g.compressWorker(ctx, encoded, compressed)
        }()
    }
    go func() {
        compressWg.Wait()
        close(compressed)
    }()
    
    // Stage 4: Network (1 goroutine, 순서 보장 필요)
    return g.streamToNetwork(ctx, compressed, output)
}
```

**대용량 파일 효과:**
- CPU 활용률: ~25% → ~80%+ (멀티코어 활용)
- 전체 처리 시간: 최대 3-4배 단축
- 메모리 효율: 버퍼 크기로 제한

### 3.5 Zero-Copy Optimization

```go
// 복사 최소화를 위한 io.Pipe 활용

func (h *UploadPackHandler) streamPackfileZeroCopy(
    ctx context.Context,
    w http.ResponseWriter,
    repo *gogit.Repository,
    wants, haves []plumbing.Hash,
) error {
    // io.Pipe로 중간 버퍼 없이 연결
    pr, pw := io.Pipe()
    
    // 생산자: packfile 생성 → pipe writer
    go func() {
        defer pw.Close()
        
        generator := NewStreamingPackfileGenerator(repo)
        if err := generator.StreamTo(ctx, pw, wants, haves); err != nil {
            pw.CloseWithError(err)
        }
    }()
    
    // 소비자: pipe reader → sideband → http response
    sbWriter := newSidebandWriter(w, 0x01)
    
    // io.Copy는 내부적으로 32KB 버퍼 사용 (복사 1회만)
    _, err := io.Copy(sbWriter, pr)
    
    return err
}

// 더 최적화된 버전: splice syscall 활용 (Linux)
func (h *UploadPackHandler) streamPackfileSplice(
    ctx context.Context,
    conn net.Conn,  // raw TCP connection
    repo *gogit.Repository,
    wants, haves []plumbing.Hash,
) error {
    // Linux에서 sendfile/splice syscall 활용 가능
    // Go의 io.Copy는 자동으로 최적화 시도
    
    file, err := h.generatePackfileToTempFile(ctx, repo, wants, haves)
    if err != nil {
        return err
    }
    defer os.Remove(file.Name())
    defer file.Close()
    
    // sendfile syscall 사용 (커널 내에서 복사)
    // Go의 net 패키지가 자동으로 처리
    _, err = io.Copy(conn, file)
    return err
}
```

**대용량 파일 효과:**
- 메모리 대역폭 절약: ~50%
- CPU 사용률 감소: ~30%
- GC 압력 감소: 임시 버퍼 할당 최소화

---

## 4. 대용량 파일 처리 개선 효과

### 4.1 시나리오별 예상 효과

#### 시나리오 A: 1GB 레포지토리, 100만 객체

| 지표 | 현재 | 개선 후 | 개선율 |
|------|------|---------|--------|
| 첫 바이트 시간 | 45초 | 3초 | **15x** |
| 전체 전송 시간 | 2분 | 45초 | **2.7x** |
| 피크 메모리 | 2GB | 200MB | **10x** |
| CPU 사용률 | 25% | 75% | **3x** |

#### 시나리오 B: 5GB 레포지토리, 500만 객체

| 지표 | 현재 | 개선 후 | 개선율 |
|------|------|---------|--------|
| 첫 바이트 시간 | 5분+ | 5초 | **60x** |
| 전체 전송 시간 | 10분+ | 3분 | **3x+** |
| 피크 메모리 | OOM 위험 | 200MB | **안정** |
| 동시 요청 처리 | 2-3개 | 10+개 | **3-5x** |

#### 시나리오 C: 느린 네트워크 (10Mbps, 200ms RTT)

| 지표 | 현재 | 개선 후 | 개선율 |
|------|------|---------|--------|
| 대역폭 활용 | 40% | 85%+ | **2x** |
| 타임아웃 발생 | 자주 | 드묾 | **안정** |
| 재시도 필요 | 자주 | 드묾 | **안정** |

### 4.2 개선 원리 요약

```
┌────────────────────────────────────────────────────────────┐
│                     개선 전 (직렬)                          │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  [전체 객체 탐색]──────────────────────────────────────→   │
│                    [전체 인코딩]──────────────────────→     │
│                                      [전체 전송]─────→     │
│                                                            │
│  메모리: ████████████████████████████████████████████      │
│          (모든 객체를 메모리에)                              │
│                                                            │
│  클라이언트: 아무것도 받지 못함... 계속 대기...              │
│                                                            │
└────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────┐
│                     개선 후 (파이프라인)                     │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  [객체 탐색]═══════════════════════════════════════════→   │
│       ↓ (스트리밍)                                         │
│       [인코딩]═════════════════════════════════════════→   │
│            ↓ (스트리밍)                                    │
│            [전송]═══════════════════════════════════════→  │
│                                                            │
│  메모리: ████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░      │
│          (버퍼만 사용, 상수 크기)                            │
│                                                            │
│  클라이언트: 즉시 데이터 수신 시작! 진행률 표시 가능!        │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### 4.3 메모리 사용량 비교

```
현재 구현:
  Memory = O(객체 수) + O(packfile 크기)
  예: 100만 객체 = ~500MB ~ 2GB+

개선 후:
  Memory = O(버퍼 크기) = 상수
  예: 100만 객체 = ~50MB ~ 200MB (버퍼 설정에 따라)

왜 이렇게 되는가?
- 현재: 모든 객체 해시를 []plumbing.Hash에 저장 후 처리
- 개선: 채널을 통해 하나씩 흘려보내며 처리, 처리된 것은 GC
```

---

## 5. 구현 로드맵

### Phase 1: 스트리밍 기반 구조 전환 (가장 큰 효과)

**목표:** Little's Law 적용, 첫 바이트 시간 단축

**작업:**
1. `StreamingPackfileGenerator` 구현
2. `revlist.Objects()` 대체 (스트리밍 버전)
3. 기존 `enc.Encode()` 대체 (스트리밍 버전)

**예상 기간:** 2-3주
**예상 효과:** 첫 바이트 시간 10x 개선, 메모리 5x 감소

### Phase 2: Backpressure 구현 (안정성)

**목표:** OOM 방지, 느린 클라이언트 지원

**작업:**
1. `BackpressureWriter` 구현
2. 기존 `sidebandWriter` 교체
3. 혼잡 제어 로직 추가

**예상 기간:** 1주
**예상 효과:** OOM 제거, 안정적인 대용량 전송

### Phase 3: 동적 청킹 (성능 최적화)

**목표:** BDP 기반 네트워크 효율 최적화

**작업:**
1. RTT 측정 로직 추가
2. 동적 청크 크기 조정 구현
3. 네트워크 조건별 테스트

**예상 기간:** 1주
**예상 효과:** 대역폭 활용률 2x 개선

### Phase 4: 파이프라인 병렬화 (처리량 최적화)

**목표:** CPU 활용률 극대화

**작업:**
1. Worker pool 기반 인코딩
2. 순서 보장 로직 (시퀀스 번호)
3. 워커 수 자동 조절

**예상 기간:** 2주
**예상 효과:** 전체 처리 시간 2-3x 단축

### Phase 5: Zero-Copy 최적화 (리소스 효율)

**목표:** 메모리/CPU 효율 극대화

**작업:**
1. io.Pipe 기반 구조로 리팩토링
2. 불필요한 버퍼 제거
3. 프로파일링 및 최적화

**예상 기간:** 1주
**예상 효과:** 메모리/CPU 30% 절감

---

## 참고 자료

### 네트워킹 법칙
- [Bandwidth-Delay Product](https://en.wikipedia.org/wiki/Bandwidth-delay_product)
- [Little's Law](https://en.wikipedia.org/wiki/Little%27s_law)
- [End-to-End Principle](https://en.wikipedia.org/wiki/End-to-end_principle)
- [TCP Congestion Control](https://en.wikipedia.org/wiki/TCP_congestion_control)

### Git Protocol
- [Git Protocol v2](https://git-scm.com/docs/protocol-v2)
- [Git Pack Protocol](https://git-scm.com/docs/pack-protocol)
- [Packfile Format](https://git-scm.com/docs/pack-format)

### Go 관련
- [go-git Library](https://github.com/go-git/go-git)
- [io.Pipe Documentation](https://pkg.go.dev/io#Pipe)
- [sync.Cond for Backpressure](https://pkg.go.dev/sync#Cond)

---

## 결론

대용량 레포지토리 처리의 핵심은 **"모든 것을 메모리에 올리지 않고 스트리밍하는 것"**입니다.

네트워킹 법칙들을 적용하면:
1. **Little's Law** → 스트리밍으로 체류시간(W) 최소화
2. **BDP** → 네트워크 파이프 최대 활용
3. **Backpressure** → 안정적인 흐름 제어
4. **Amdahl's Law** → 병렬화로 처리량 극대화
5. **Zero-Copy** → 리소스 효율 최적화

이를 통해 **수 GB 레포지토리도 수백 MB 메모리로 안정적으로 처리**할 수 있습니다.


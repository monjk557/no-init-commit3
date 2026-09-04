# Post-receive Hook Migration Guide

## Current Architecture (Snapshot Diff)

현재 `handleReceivePack`은 native `git receive-pack` 실행 전후로 ref 스냅샷을 비교하여
변경된 ref를 감지하고 이벤트를 발행합니다.

```
Go Server
  ├─ git for-each-ref (snapshot before)
  ├─ git receive-pack (native, bidirectional pipe)
  ├─ git for-each-ref (snapshot after)
  ├─ diff → publish events to Redis
  └─ cache invalidation
```

### Snapshot Diff 방식의 한계

- push 전후로 `git for-each-ref`를 2회 실행 (소규모 레포에서는 무시할 수준)
- repo write lock에 의존하여 race condition 방지
- ref 변경의 "원인"을 알 수 없음 (push에 의한 것인지, 다른 작업인지)

---

## Target Architecture (Post-receive Hook)

Git의 표준 `post-receive` hook을 사용하여 ref 변경 정보를 실시간으로 수신합니다.

```
Git receive-pack process
  └─ refs 업데이트 완료
      └─ hooks/post-receive 실행
          └─ stdin: "old-hash new-hash refname" (per ref)
              └─ Unix socket → Go server → Redis event publish
```

---

## Migration Steps

### Phase 1: Hook Infrastructure

#### 1.1 공유 hooks 디렉토리 생성

```bash
mkdir -p /opt/collabops/hooks
```

#### 1.2 post-receive hook 스크립트 작성

```bash
#!/bin/sh
# /opt/collabops/hooks/post-receive
#
# Reads ref updates from stdin and sends them to the Go server
# via Unix socket for event publishing.
#
# stdin format (one line per ref):
#   <old-hash> <new-hash> <refname>
#
# Environment variables set by the Go server:
#   COLLABOPS_SOCKET  - Unix socket path
#   COLLABOPS_OWNER   - repository owner
#   COLLABOPS_REPO    - repository name
#   COLLABOPS_USER_ID - user who pushed

SOCKET="${COLLABOPS_SOCKET:-/var/run/collabops/hooks.sock}"

# Collect all ref updates
PAYLOAD=""
while read oldrev newrev refname; do
  PAYLOAD="${PAYLOAD}${oldrev} ${newrev} ${refname}\n"
done

# Send to Go server via Unix socket (non-blocking, fire-and-forget)
if [ -S "$SOCKET" ]; then
  printf "%s" "$PAYLOAD" | nc -U -w1 "$SOCKET" 2>/dev/null || true
fi
```

```bash
chmod +x /opt/collabops/hooks/post-receive
```

#### 1.3 Alternative: Compiled Go Hook Binary

성능이 중요한 경우, shell 대신 Go 바이너리를 hook으로 사용할 수 있습니다.

```go
// cmd/post-receive-hook/main.go
package main

import (
    "bufio"
    "net"
    "os"
    "strings"
)

func main() {
    socketPath := os.Getenv("COLLABOPS_SOCKET")
    if socketPath == "" {
        socketPath = "/var/run/collabops/hooks.sock"
    }

    conn, err := net.Dial("unix", socketPath)
    if err != nil {
        os.Exit(0) // fail silently — don't block push
    }
    defer conn.Close()

    // Forward stdin (ref updates) to Unix socket
    scanner := bufio.NewScanner(os.Stdin)
    for scanner.Scan() {
        conn.Write([]byte(scanner.Text() + "\n"))
    }
}
```

### Phase 2: Go Server Side

#### 2.1 Unix Socket Listener 추가

`DuplexTCPServer`에 Unix socket listener를 추가하여 hook으로부터 ref 변경 정보를 수신합니다.

```go
// internal/server/hook_listener.go

type HookListener struct {
    socketPath string
    listener   net.Listener
    server     *DuplexTCPServer
    logger     *logging.Logger
}

func NewHookListener(socketPath string, server *DuplexTCPServer) (*HookListener, error) {
    // Remove stale socket
    os.Remove(socketPath)

    listener, err := net.Listen("unix", socketPath)
    if err != nil {
        return nil, err
    }

    // Set permissions so git process can connect
    os.Chmod(socketPath, 0660)

    return &HookListener{
        socketPath: socketPath,
        listener:   listener,
        server:     server,
    }, nil
}

func (h *HookListener) Start() {
    go func() {
        for {
            conn, err := h.listener.Accept()
            if err != nil {
                return
            }
            go h.handleHookMessage(conn)
        }
    }()
}

func (h *HookListener) handleHookMessage(conn net.Conn) {
    defer conn.Close()

    scanner := bufio.NewScanner(conn)
    for scanner.Scan() {
        line := scanner.Text()
        parts := strings.Fields(line)
        if len(parts) != 3 {
            continue
        }
        oldHash, newHash, refName := parts[0], parts[1], parts[2]

        // Publish event using existing infrastructure
        // (owner, repo, userID come from environment variables
        //  set when spawning git receive-pack)
    }
}
```

#### 2.2 handleReceivePack 수정

```go
func (s *DuplexTCPServer) handleReceivePack(conn *connection.ProgressAwareConnection, owner, repo string) error {
    // ... concurrency control, repo lock ...

    _, _, userID, _ := conn.GetConnectionInfo()

    cmd := exec.CommandContext(ctx, "git",
        "-c", "core.hooksPath=/opt/collabops/hooks",
        "receive-pack", repoPath)

    // Pass context to hook via environment
    cmd.Env = append(cmd.Environ(),
        "COLLABOPS_SOCKET=/var/run/collabops/hooks.sock",
        "COLLABOPS_OWNER="+owner,
        "COLLABOPS_REPO="+repo,
        "COLLABOPS_USER_ID="+userID,
    )

    cmd.Stdin = conn.GetReader()
    cmd.Stdout = &flushWriter{conn.GetWriter()}

    if err := cmd.Run(); err != nil {
        return fmt.Errorf("git receive-pack: %w", err)
    }

    // Cache invalidation (still done here — hook handles events only)
    repositories.GetSimpleCache().Invalidate(repoPath)

    return nil
}
```

### Phase 3: Cleanup

1. `snapshotRefs()` 삭제
2. `publishRefChangeEvents()` 삭제
3. `handleReceivePack`에서 snapshot 관련 코드 삭제
4. Hook listener의 이벤트 발행 로직으로 완전 이관

---

## Comparison

| | Snapshot Diff (현재) | Post-receive Hook (미래) |
|---|---|---|
| Ref 변경 감지 | for-each-ref 2회 | Git이 직접 전달 |
| 추가 프로세스 | 없음 | hook process 1개 |
| 통신 채널 | 불필요 | Unix socket |
| 배포 복잡도 | 없음 | hook 스크립트 + socket |
| 정확도 | snapshot diff (lock 보호) | Git 내부에서 직접 전달 |
| 클라이언트 지연 | push 후 for-each-ref 시간 | hook 실행 시간 |
| 확장성 | 대규모 ref에서 snapshot 비용 증가 | ref 수와 무관 (변경분만) |

---

## When to Migrate

현재 snapshot diff 방식은 다음 조건에서 충분합니다:
- ref 수가 수천 개 이하
- repo write lock이 유지되는 한 race condition 없음
- push 빈도가 초당 수십 건 이하

다음 조건이 되면 hook 방식으로 이관을 고려하세요:
- ref 수가 만 단위 이상으로 증가하여 for-each-ref 비용이 체감됨
- repo write lock을 제거하고 더 세밀한 동시성 제어가 필요
- push 외의 ref 변경 (예: server-side ref update)도 감지해야 함

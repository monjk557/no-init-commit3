# Fork API Handoff - 웹 프론트엔드 연동 가이드

> **Branch**: `feature/repository-fork`
> **Base URL**: `{GIT_EXECUTOR_HOST}/repos`
> **공통 헤더**: `X-USER-ID` (현재 로그인 유저 ID)

---

## 1. 저장소 포크 생성

### `POST /repos/{owner}/{repo}/forks`

소스 저장소를 다른 owner 아래로 복제합니다.

**Request**

```
POST /repos/team-a/my-project/forks
Content-Type: application/json
X-USER-ID: user-123

{
  "owner": "user-123",
  "repo": "my-project",
  "default_branch": "main"
}
```

| 필드 | 타입 | 필수 | 설명 |
|------|------|------|------|
| `owner` | string | O | 포크 대상 소유자 (보통 현재 유저의 workspace) |
| `repo` | string | X | 포크 저장소 이름. 미지정 시 원본과 동일 |
| `default_branch` | string | X | 기본 브랜치 오버라이드. 미지정 시 원본 HEAD 따름 |

**Response `201 Created`**

```json
{
  "success": true,
  "repository": "user-123/my-project",
  "source_repository": "team-a/my-project",
  "owner": "user-123",
  "name": "my-project",
  "default_branch": "main",
  "created_at": "2026-03-19T10:00:00Z",
  "fork_metadata": {
    "source_owner": "team-a",
    "source_repo": "my-project",
    "source_path": "/var/lib/git/repos/team-a/my-project.git",
    "forked_at": "2026-03-19T10:00:00Z",
    "forked_by": "user-123"
  }
}
```

**에러 응답**

| Status | 조건 |
|--------|------|
| `400` | 잘못된 owner/repo 이름, JSON 파싱 실패 |
| `404` | 소스 저장소가 존재하지 않음 |
| `409` | 타겟 경로에 이미 저장소가 존재 |
| `500` | git clone 실패 등 내부 오류 |

---

## 2. 포크 정보 조회

### `GET /repos/{owner}/{repo}/fork`

저장소가 포크인지 확인하고, 포크라면 원본 저장소 정보를 반환합니다.
UI에서 "Forked from team-a/my-project" 같은 라벨을 표시할 때 사용합니다.

**Request**

```
GET /repos/user-123/my-project/fork
```

**Response `200 OK` - 포크인 경우**

```json
{
  "success": true,
  "is_fork": true,
  "repository": "user-123/my-project",
  "fork_metadata": {
    "source_owner": "team-a",
    "source_repo": "my-project",
    "source_path": "/var/lib/git/repos/team-a/my-project.git",
    "forked_at": "2026-03-19T10:00:00Z",
    "forked_by": "user-123"
  }
}
```

**Response `200 OK` - 포크가 아닌 경우**

```json
{
  "success": true,
  "is_fork": false,
  "repository": "user-123/my-project"
}
```

> `is_fork` 필드로 분기하면 됩니다. 포크가 아니어도 200을 반환합니다.

**에러 응답**

| Status | 조건 |
|--------|------|
| `400` | 잘못된 경로 |
| `404` | 저장소가 존재하지 않음 |

---

## 3. 업스트림 동기화

### `POST /repos/{owner}/{repo}/fork/sync`

포크된 저장소의 브랜치들을 원본(upstream)의 최신 상태로 동기화합니다.
Fast-forward 가능한 브랜치만 자동 업데이트하고, diverge된 브랜치는 conflicts로 리포트합니다.

**Request**

```
POST /repos/user-123/my-project/fork/sync
```

> Request body 없음.

**Response `200 OK`**

```json
{
  "success": true,
  "branches_synced": [
    {
      "name": "main",
      "old_commit_sha": "abc1234...",
      "new_commit_sha": "def5678...",
      "commits_behind": 3
    },
    {
      "name": "develop",
      "old_commit_sha": "111aaaa...",
      "new_commit_sha": "222bbbb...",
      "commits_behind": 7
    }
  ],
  "conflicts": [
    {
      "name": "feature/my-work",
      "reason": "branches have diverged, manual merge required"
    }
  ]
}
```

| 필드 | 설명 |
|------|------|
| `branches_synced` | fast-forward 성공한 브랜치 목록. 빈 배열이면 이미 최신 |
| `branches_synced[].commits_behind` | 동기화 전 뒤처져 있던 커밋 수 |
| `conflicts` | 자동 동기화 불가능한 브랜치 목록 (diverge 등) |

**에러 응답**

| Status | 조건 |
|--------|------|
| `400` | 해당 저장소가 포크가 아님 (`repository is not a fork`) |
| `404` | 저장소가 없거나, 원본 저장소가 삭제됨 (`upstream repository no longer exists`) |
| `500` | fetch 실패 등 내부 오류 |

---

## 웹 UI 연동 시나리오

### 시나리오 1: 포크 버튼

```
[Fork] 버튼 클릭
  → POST /repos/{sourceOwner}/{sourceRepo}/forks
    body: { "owner": currentUser.workspace }
  → 성공 시 /repos/{currentUser.workspace}/{repo} 페이지로 이동
  → 409 시 "이미 포크된 저장소가 있습니다" 안내
```

### 시나리오 2: "Forked from" 라벨

```
저장소 페이지 진입 시
  → GET /repos/{owner}/{repo}/fork
  → is_fork === true 이면
    "Forked from {fork_metadata.source_owner}/{fork_metadata.source_repo}" 표시
    + [Sync] 버튼 노출
```

### 시나리오 3: Upstream Sync

```
[Sync with upstream] 버튼 클릭
  → POST /repos/{owner}/{repo}/fork/sync
  → branches_synced.length > 0 이면
    "{N}개 브랜치가 업데이트되었습니다" 토스트
  → branches_synced.length === 0 && conflicts.length === 0 이면
    "이미 최신 상태입니다" 토스트
  → conflicts.length > 0 이면
    충돌 브랜치 목록 표시 + "수동 머지가 필요합니다" 안내
```

---

## Redis Stream 이벤트

포크 생성 시 Redis Stream에 이벤트가 발행됩니다.

```json
{
  "event_type": "fork",
  "repo_path": "user-123/my-project.git",
  "ref": "refs/heads/main",
  "user_id": "user-123",
  "commit_message": "Forked from team-a/my-project"
}
```

이벤트 구독으로 실시간 알림, 활동 로그 등에 활용할 수 있습니다.

---

## 제한 사항 / 알아둘 점

1. **대용량 저장소**: `git clone --bare`로 전체 복제하므로 큰 저장소는 응답이 느릴 수 있음. 프론트에서 로딩 상태 처리 필요.
2. **LFS 객체**: LFS 포인터 파일은 복제되지만, GCS의 실제 LFS 객체는 별도 공유/복사 로직이 아직 없음. 포크에서 LFS 파일 다운로드 시 원본 저장소의 LFS 설정 필요.
3. **Cross-repo PR**: 포크→원본으로의 Pull Request(cross-repo merge) 기능은 아직 미구현. 현재는 동일 저장소 내 브랜치 간 머지만 지원.
4. **Fork of Fork**: 포크의 포크도 가능하지만, sync는 직접 부모만 추적함 (원본의 원본까지 체이닝하지 않음).
5. **원본 삭제**: 원본이 삭제되면 포크 자체는 독립적으로 동작하나, sync 호출 시 404 반환.

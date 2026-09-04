# Branch Resolve API — 설계서 및 사용설명서

---

# 1. 설계서 (Branch Resolve API)

## 1.1 개요

| 항목 | 내용 |
|------|------|
| API 이름 | Branch Resolve (브랜치 → 커밋 SHA 조회) |
| 목적 | 저장소 내 **브랜치 이름**을 입력받아, 해당 브랜치가 가리키는 **커밋 SHA**와 ref 경로를 반환 |
| 메서드 | GET |
| 인증 | (서비스 정책에 따름, 본 설계에서는 미정의) |

## 1.2 사용 사례

- CI/CD·자동화에서 "특정 브랜치의 최신 커밋 SHA"가 필요할 때
- 브랜치 이름만 알고 있을 때, 해당 브랜치의 정확한 커밋 해시 조회
- 머지·배포 파이프라인에서 소스/타겟 브랜치의 SHA 검증

## 1.3 엔드포인트

**URL 패턴**

```
GET /repos/{owner}/{repo}/branches/resolve/{ref}
```

| 경로 변수 | 설명 | 예시 |
|-----------|------|------|
| owner | 저장소 소유자(또는 그룹/경로) | `myorg` |
| repo | 저장소 이름 | `my-repo` |
| ref | 브랜치 이름 (앞쪽 `/`는 무시) | `main`, `feature/foo` |

**참고**

- `ref`는 **브랜치 이름**만 지원합니다. `refs/heads/` 접두어 없이 브랜치 이름만 넣습니다.
- `HEAD`, 태그명, 원격 ref 등은 이 API에서 **지원하지 않습니다**.

## 1.4 요청

- **쿼리 파라미터**: 없음
- **Request body**: 없음
- **Headers**: (서비스 공통 정책 적용)

## 1.5 응답

### 성공 (200 OK)

| 필드 | 타입 | 설명 |
|------|------|------|
| success | boolean | 항상 `true` |
| name | string | 요청한 브랜치 이름 (그대로 반환) |
| commit_sha | string | 해당 브랜치가 가리키는 커밋의 SHA (40자 16진수) |
| ref_path | string | Git ref 전체 경로. 항상 `refs/heads/{name}` 형식 |

**예시**

```json
{
  "success": true,
  "name": "main",
  "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "ref_path": "refs/heads/main"
}
```

### 에러 응답

| HTTP 상태 | success | error 메시지 (요지) | 발생 조건 |
|-----------|---------|---------------------|-----------|
| 400 | false | branch name is required | ref가 비어 있음 (예: `/resolve/` 만 호출) |
| 400 | false | Invalid path: ... | owner/repo 경로가 유효하지 않음 |
| 404 | false | repository not found | 저장소가 없거나 열 수 없음 |
| 404 | false | branch '{ref}' not found | 해당 이름의 브랜치가 없음 |
| 422 | false | repository corrupted | 저장소 손상 등 내부 오류 |

에러 시 공통 형식 예:

```json
{
  "success": false,
  "error": "branch 'feature/xyz' not found"
}
```

## 1.6 동작 방식 (설계 요지)

- `ref`를 `refs/heads/{ref}` 로 해석해, **로컬 브랜치 참조만** 조회합니다.
- 해당 ref가 존재하면 그 참조가 가리키는 **commit hash**를 반환합니다.
- 브랜치가 없으면 404, 저장소를 열 수 없거나 손상이면 404/422를 반환합니다.

## 1.7 제한 사항

- **브랜치 이름만 지원**: `HEAD`, `refs/tags/...`, `refs/remotes/...` 등은 미지원.
- **저장소 경로**: 서버의 path 정책(owner/repo → 디스크 경로)에 의존.

---

# 2. 사용설명서 (Branch Resolve API)

## 2.1 이 API로 할 수 있는 것

- **브랜치 이름**을 넣으면, 그 브랜치가 가리키는 **커밋 SHA**를 받을 수 있습니다.
- 예: "main 브랜치의 현재 커밋이 뭐지?" → 한 번의 GET 요청으로 SHA 확인.

## 2.2 호출 방법

**기본 형태**

```
GET {베이스URL}/repos/{owner}/{repo}/branches/resolve/{브랜치이름}
```

- `{베이스URL}`: 서비스 루트 (예: `https://git-api.example.com`)
- `{owner}`, `{repo}`: 저장소 식별자
- `{브랜치이름}`: `main`, `develop`, `feature/issue-123` 처럼 **이름만** 입력

**주의**

- 브랜치 이름에 `/`가 있으면 그대로 URL에 넣으면 됩니다. (예: `feature/issue-123`)
- URL 인코딩이 필요한 문자는 인코딩해서 보냅니다.

## 2.3 호출 예시

**예 1: main 브랜치의 SHA 조회**

- 요청: `GET /repos/myorg/my-repo/branches/resolve/main`
- 성공 시 (200) 예시:

```json
{
  "success": true,
  "name": "main",
  "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "ref_path": "refs/heads/main"
}
```

**예 2: feature 브랜치 조회**

- 요청: `GET /repos/myorg/my-repo/branches/resolve/feature/login`
- 성공 시 (200) 예시:

```json
{
  "success": true,
  "name": "feature/login",
  "commit_sha": "b2c3d4e5f6789012345678901234567890abcde",
  "ref_path": "refs/heads/feature/login"
}
```

**예 3: 없는 브랜치**

- 요청: `GET /repos/myorg/my-repo/branches/resolve/nonexistent`
- 응답: 404

```json
{
  "success": false,
  "error": "branch 'nonexistent' not found"
}
```

## 2.4 응답 필드 활용

| 필드 | 활용 예 |
|------|----------|
| success | true일 때만 아래 필드 사용 |
| name | 요청한 브랜치 이름 확인 |
| commit_sha | 배포·빌드·머지 기준 커밋으로 사용, 캐시 키로 사용 |
| ref_path | Git 클라이언트나 다른 도구에 ref 전체 경로로 전달 |

## 2.5 자주 나오는 에러와 대처

| 상황 | HTTP | 대처 |
|------|------|------|
| ref를 비워서 호출 (예: `/resolve/`) | 400 | ref 자리에 브랜치 이름을 넣어 다시 요청 |
| 저장소가 없음 | 404 | owner/repo 확인, 저장소 생성 여부 확인 |
| 브랜치가 없음 | 404 | 브랜치 이름 오타·대소문자 확인, 해당 브랜치 존재 여부 확인 |
| repository corrupted | 422 | 저장소 무결성 점검, 관리자 문의 |

## 2.6 cURL 예시

```bash
# main 브랜치 resolve
curl -X GET "https://git-api.example.com/repos/myorg/my-repo/branches/resolve/main"

# feature 브랜치 (슬래시 포함)
curl -X GET "https://git-api.example.com/repos/myorg/my-repo/branches/resolve/feature/login"
```

---

*문서 버전: 1.0 (Branch Resolve API 기준)*

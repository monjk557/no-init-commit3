# CollabOps 벤치마킹 빠른 시작 가이드

## 🚀 5분 만에 벤치마크 실행하기

### 1. 환경 변수 설정

```bash
# GitHub API 토큰 (필수)
export GITHUB_TOKEN="your_github_token_here"

# GitLab 토큰 (옵션 - 로컬 GitLab 사용시)
export GITLAB_TOKEN="your_gitlab_token_here"

# Gitea 토큰 (옵션)
export GITEA_TOKEN="your_gitea_token_here"
```

### 2. 벤치마킹 환경 시작

```bash
# 벤치마킹 인프라 디렉토리로 이동
cd docs/benchmarking/infrastructure

# 모든 플랫폼 시작 (5-10분 소요)
docker-compose up -d

# 서비스 상태 확인
docker-compose ps
```

### 3. 플랫폼 준비 확인

```bash
# GitHub API 확인
curl -H "Authorization: token $GITHUB_TOKEN" \
     "https://api.github.com/rate_limit"

# GitLab 확인 (로컬)
curl http://localhost:8080/api/v4/projects

# CollabOps 확인
curl http://localhost:8090/health

# Gitea 확인
curl http://localhost:3000/api/v1/version
```

### 4. 벤치마크 실행

```bash
# 전체 벤치마크 실행 (10-20분 소요)
./benchmark-runner/run-all-benchmarks.sh

# 결과 확인
ls -la /tmp/benchmark-results*/
```

## 📊 결과 해석

### 생성되는 파일들

```
benchmark-results_20260224_143022/
├── raw-data/
│   ├── github-results.json      # GitHub API 성능 데이터
│   ├── gitlab-results.json      # GitLab CE 성능 데이터
│   ├── collabops-results.json   # CollabOps 성능 데이터
│   └── concurrent-load-results.json # 동시 부하 테스트 결과
├── analysis/
│   └── summary.md               # 자동 분석 요약
└── charts/
    └── (차후 시각화 파일들)
```

### 핵심 메트릭 확인

```bash
# 각 플랫폼의 평균 응답시간 비교
jq -r '.tests[] | .duration' raw-data/github-results.json | awk '{sum+=$1; count++} END {print "GitHub 평균:", sum/count "초"}'
jq -r '.tests[] | .duration' raw-data/gitlab-results.json | awk '{sum+=$1; count++} END {print "GitLab 평균:", sum/count "초"}'
jq -r '.tests[] | .duration' raw-data/collabops-results.json | awk '{sum+=$1; count++} END {print "CollabOps 평균:", sum/count "초"}'
```

## 🔧 문제 해결

### 일반적인 문제들

#### **Docker 서비스 시작 실패**
```bash
# 로그 확인
docker-compose logs gitlab
docker-compose logs collabops

# 메모리/디스크 공간 확인
docker system df
df -h
```

#### **GitLab 초기 설정**
GitLab CE는 처음 시작시 5-10분의 초기화 시간이 필요합니다.

```bash
# GitLab 초기화 상태 확인
docker-compose logs gitlab | grep "Gitlab Reconfigure"

# 웹 UI 접속 (초기 비밀번호 확인)
docker exec -it benchmark-gitlab cat /etc/gitlab/initial_root_password
```

#### **Rate Limiting 문제**
```bash
# GitHub rate limit 확인
curl -H "Authorization: token $GITHUB_TOKEN" \
     "https://api.github.com/rate_limit" | jq '.rate'

# 시간당 5000회 제한 - 부족하면 대기 또는 테스트 규모 축소
```

### 성능 기준 참고

#### **예상 성능 범위**
| 플랫폼 | 디렉토리 리스팅 | 커밋 히스토리 |
|--------|-----------------|---------------|
| **GitHub** | 250-350ms | 400-600ms |
| **GitLab CE** | 150-250ms | 200-400ms |
| **CollabOps (소형)** | 200-300ms | 150-250ms |
| **CollabOps (대형)** | 1-3초 | 2-10초 |

#### **성능 이상 판단 기준**
- ✅ **정상**: 위 범위 내 성능
- ⚠️ **주의**: 2배 이상 느린 성능
- ❌ **문제**: 5배 이상 느리거나 에러 발생

## 🎯 다음 단계

### 1. 상세 분석
```bash
# Python/Jupyter로 데이터 분석
pip install pandas matplotlib seaborn
jupyter notebook analysis.ipynb
```

### 2. 정기적 모니터링
```bash
# 크론잡 설정 (매일 새벽 2시)
0 2 * * * cd /path/to/benchmarking && ./run-all-benchmarks.sh
```

### 3. 성능 회귀 감지
```bash
# 이전 결과와 비교
python compare_results.py results-yesterday/ results-today/
```

## 📞 지원

문제 발생시:
1. 이 문서의 문제해결 섹션 확인
2. Docker 로그 확인 (`docker-compose logs`)
3. GitHub Issues에 문의 (로그와 함께)

**Happy Benchmarking!** 🚀
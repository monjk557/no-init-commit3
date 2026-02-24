# Phase 2 구현 시작 체크리스트

## ✅ 시작하기 전 확인사항

### **개발 환경 준비**
- [ ] Go 1.19+ 설치 확인
- [ ] Redis 서버 실행 (로컬 또는 Docker)
- [ ] PostgreSQL/SQLite 데이터베이스 준비
- [ ] 기존 CollabOps 서버 정상 동작 확인

### **현재 코드베이스 파악**
- [ ] `/internal/overview/service.go` 위치 확인
- [ ] `/internal/handlers/` 구조 파악
- [ ] 기존 캐시 시스템 (`internal/cache/`) 확인
- [ ] Git 접근 레이어 (`git2go` 사용 부분) 파악

## 🚀 Week 1 시작: 스마트 페이지네이션

### **Day 1: 구조체 정의**
```bash
# 1. 새로운 타입 정의 파일 생성
touch internal/overview/types.go

# 2. 페이지네이션 로직 파일 생성
touch internal/overview/pagination.go

# 3. 성능 모니터링 파일 생성
touch internal/monitoring/performance.go
```

### **Day 1 체크리스트**
- [ ] `DirectoryResponse`에 `Pagination` 필드 추가
- [ ] `DirectoryRequest`에 `Page`, `PerPage` 필드 추가
- [ ] `SmartPagination` 구조체 구현
- [ ] 기본 페이지 크기 결정 로직 구현

### **Day 2: 서비스 레이어**
```bash
# 기존 서비스 백업
cp internal/overview/service.go internal/overview/service.go.backup

# 새 메서드 추가
# - GetDirectoryTreeWithCommits() 수정
# - applyPagination() 추가
# - estimateDirectorySize() 추가
```

### **Day 2 체크리스트**
- [ ] 기존 `GetDirectoryTree` 메서드 수정
- [ ] 페이지네이션 적용 로직 구현
- [ ] 전체 파일 목록 → 페이지별 분할 로직
- [ ] 응답 메타데이터 (total, pages) 생성

### **Day 3: API 엔드포인트**
```bash
# HTTP 핸들러 수정
cp internal/handlers/overview.go internal/handlers/overview.go.backup

# 쿼리 파라미터 파싱 로직 추가
# - ?page=1&per_page=50 파싱
# - 응답 헤더 추가 (X-Total, X-Page 등)
```

### **Day 3 체크리스트**
- [ ] 쿼리 파라미터 (`page`, `per_page`) 파싱
- [ ] 응답 헤더 설정 (GitLab 패턴)
- [ ] 에러 처리 및 유효성 검증
- [ ] API 문서 업데이트

### **Day 4-5: 테스트 및 검증**
```bash
# 테스트 작성
touch internal/overview/pagination_test.go
touch internal/handlers/overview_test.go

# 성능 측정
./docs/benchmarking/infrastructure/benchmark-runner/run-all-benchmarks.sh
```

### **Week 1 성공 기준**
- [ ] **중형 저장소**: 567ms → 200ms 달성
- [ ] **API 호환성**: 기존 클라이언트 동작 유지
- [ ] **페이지네이션**: 1000개 파일을 50개씩 20페이지로 분할
- [ ] **자동 최적화**: 저장소 크기에 따른 적응형 페이지 크기

---

## 🚀 Week 2 시작: 다층 캐싱

### **사전 준비**
```bash
# Redis 설치 및 실행
docker run -d --name redis -p 6379:6379 redis:7-alpine

# Go Redis 라이브러리 설치
go mod tidy
```

### **Day 6: Redis 캐시 레이어**
```bash
# 캐시 패키지 생성
mkdir -p internal/cache
touch internal/cache/redis.go
touch internal/cache/memory.go
touch internal/cache/multi_tier.go
```

### **Day 6 체크리스트**
- [ ] Redis 연결 설정
- [ ] JSON 직렬화/역직렬화
- [ ] TTL 기반 자동 만료
- [ ] 연결 풀링 및 에러 처리

### **Day 7-8: 다층 캐시 통합**
- [ ] L1 (메모리) + L2 (Redis) 계층 구현
- [ ] 캐시 키 설계 (`dir:repo:ref:path` 형태)
- [ ] 캐시 히트/미스 메트릭 수집
- [ ] 성능 모니터링 대시보드

### **Day 9-10: 서비스 통합**
```bash
# 설정 파일 업데이트
# config/config.yaml에 Redis 설정 추가

# 의존성 주입
# main.go에서 캐시 레이어 초기화
```

### **Week 2 성공 기준**
- [ ] **캐시 적중률**: 90% → 98% 달성
- [ ] **반복 요청**: 40% 성능 향상
- [ ] **메모리 효율**: L1/L2 계층 적절한 분산
- [ ] **장애 복구**: Redis 다운 시에도 정상 동작

---

## 🚀 Week 3 시작: 커밋 인덱싱

### **사전 준비**
```bash
# 데이터베이스 마이그레이션 도구
go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# GORM 설치 확인
go mod tidy
```

### **Day 11: 데이터베이스 설계**
```bash
# 마이그레이션 파일 생성
mkdir -p migrations
touch migrations/001_create_commit_path_index.up.sql
touch migrations/001_create_commit_path_index.down.sql

# 모델 파일 생성
touch internal/models/commit_index.go
```

### **Day 11 체크리스트**
- [ ] 테이블 스키마 정의
- [ ] 인덱스 설계 (성능 최적화)
- [ ] GORM 모델 정의
- [ ] 마이그레이션 스크립트 작성

### **Day 12-13: 인덱스 빌더**
```bash
# 인덱싱 패키지 생성
mkdir -p internal/indexing
touch internal/indexing/builder.go
touch internal/indexing/query.go
touch internal/indexing/git_interface.go
```

### **Day 12-13 체크리스트**
- [ ] 전체 저장소 초기 인덱스 구축
- [ ] 배치 처리 (메모리 효율성)
- [ ] Upsert 로직 (기존 데이터 업데이트)
- [ ] 증분 업데이트 (Push 훅 연동)

### **Day 14-15: 고속 조회 서비스**
```bash
# 기존 서비스와 통합
cp internal/overview/service.go internal/overview/service_with_index.go

# 관리자 API 추가
touch internal/handlers/admin.go
```

### **Day 14-15 체크리스트**
- [ ] 단일 SQL 쿼리로 다중 경로 조회
- [ ] 커밋 메타데이터 + Git 상세정보 결합
- [ ] 인덱스 재구축 관리 API
- [ ] 인덱스 상태 모니터링

### **Week 3 성공 기준**
- [ ] **커밋 히스토리**: 8,450ms → 200ms 달성
- [ ] **인덱스 구축**: 1만 커밋을 5분 내 인덱싱
- [ ] **실시간 업데이트**: Push 시 즉시 인덱스 반영
- [ ] **데이터 정합성**: Git과 인덱스 데이터 100% 일치

---

## 🎯 최종 검증 및 배포

### **성능 벤치마크**
```bash
# 기존 벤치마킹 시스템 활용
cd docs/benchmarking/infrastructure
./benchmark-runner/run-all-benchmarks.sh

# Phase 2 전후 비교
python compare_performance.py baseline/ phase2/
```

### **배포 전 체크리스트**
- [ ] 모든 기존 테스트 통과
- [ ] 새로운 기능 테스트 100% 커버리지
- [ ] 메모리 누수 검사 완료
- [ ] 부하 테스트 통과 (100 동시 사용자)
- [ ] 롤백 계획 수립

### **최종 성과 확인**
- [ ] **중형 저장소**: 567ms → 150ms (74% 개선) ✅
- [ ] **대형 저장소**: 2,340ms → 300ms (87% 개선) ✅
- [ ] **동시 사용자**: 834ms → 300ms (64% 개선) ✅
- [ ] **캐시 적중률**: 90% → 98% (8% 개선) ✅

---

## 🚨 주의사항 및 팁

### **개발 시 주의사항**
1. **Git Pack/Unpack 관련 코드는 건드리지 말 것** (다른 팀 담당)
2. **기존 API 호환성 유지** (Breaking Changes 금지)
3. **단계별 배포** (한 번에 모든 기능 배포 금지)
4. **성능 모니터링** (각 단계마다 성능 측정)

### **성공을 위한 팁**
1. **작은 단위로 커밋** (기능별로 나누어 커밋)
2. **테스트 우선 작성** (TDD 방식 권장)
3. **성능 회귀 방지** (벤치마크를 CI/CD에 통합)
4. **문서화** (코드 변경 시 문서 업데이트)

---

## 🆘 문제 해결 가이드

### **자주 발생하는 문제들**

#### **Redis 연결 문제**
```bash
# Redis 상태 확인
docker ps | grep redis
redis-cli ping

# 연결 설정 확인
go run main.go --config-check
```

#### **데이터베이스 마이그레이션 실패**
```bash
# 마이그레이션 상태 확인
migrate -path migrations -database "postgres://..." version

# 마이그레이션 롤백
migrate -path migrations -database "postgres://..." down 1
```

#### **성능 목표 미달성**
```bash
# 프로파일링 실행
go tool pprof http://localhost:8090/debug/pprof/profile

# 메모리 사용량 확인
go tool pprof http://localhost:8090/debug/pprof/heap
```

**이제 시작할 준비가 완료되었습니다! 🚀**

첫 번째 스텝부터 차근차근 시작해보세요!
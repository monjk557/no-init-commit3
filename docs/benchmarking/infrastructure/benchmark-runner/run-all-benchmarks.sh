#!/bin/bash

# CollabOps Comprehensive Benchmark Suite
# 모든 플랫폼에 대한 통합 성능 테스트 실행

set -euo pipefail

# 색깔 출력을 위한 설정
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 로깅 함수
log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# 설정
RESULTS_DIR="/results/$(date +%Y%m%d_%H%M%S)"
PLATFORMS=("github" "gitlab" "gitea" "collabops")
TEST_SCENARIOS=("directory-listing" "commit-history" "concurrent-load")

# 환경 변수 확인
check_environment() {
    log_info "환경 변수 확인 중..."

    local missing_vars=()

    [[ -z "${GITHUB_TOKEN:-}" ]] && missing_vars+=("GITHUB_TOKEN")
    [[ -z "${GITLAB_TOKEN:-}" ]] && missing_vars+=("GITLAB_TOKEN")
    [[ -z "${GITEA_TOKEN:-}" ]] && missing_vars+=("GITEA_TOKEN")

    if [[ ${#missing_vars[@]} -gt 0 ]]; then
        log_warning "다음 환경변수가 누락되었습니다: ${missing_vars[*]}"
        log_warning "일부 플랫폼 테스트가 제한될 수 있습니다."
    fi
}

# 플랫폼 상태 확인
check_platform_health() {
    log_info "플랫폼 상태 확인 중..."

    local failed_platforms=()

    # GitHub API 확인
    if ! curl -s -f -H "Authorization: token ${GITHUB_TOKEN:-}" \
         "https://api.github.com/rate_limit" >/dev/null; then
        failed_platforms+=("GitHub")
    fi

    # GitLab 확인
    if ! curl -s -f "${GITLAB_URL:-http://gitlab:80}/health" >/dev/null; then
        failed_platforms+=("GitLab")
    fi

    # Gitea 확인
    if ! curl -s -f "${GITEA_URL:-http://gitea:3000}/api/v1/version" >/dev/null; then
        failed_platforms+=("Gitea")
    fi

    # CollabOps 확인
    if ! curl -s -f "${COLLABOPS_URL:-http://collabops:8090}/health" >/dev/null; then
        failed_platforms+=("CollabOps")
    fi

    if [[ ${#failed_platforms[@]} -gt 0 ]]; then
        log_error "다음 플랫폼이 응답하지 않습니다: ${failed_platforms[*]}"
        log_error "docker-compose up -d로 모든 서비스를 시작했는지 확인하세요."
        exit 1
    fi

    log_success "모든 플랫폼이 정상 상태입니다."
}

# 결과 디렉토리 생성
setup_results_directory() {
    log_info "결과 디렉토리 설정: $RESULTS_DIR"
    mkdir -p "$RESULTS_DIR"
    mkdir -p "$RESULTS_DIR/raw-data"
    mkdir -p "$RESULTS_DIR/analysis"
    mkdir -p "$RESULTS_DIR/charts"
}

# GitHub 벤치마크
benchmark_github() {
    log_info "GitHub API 벤치마크 실행 중..."

    local output_file="$RESULTS_DIR/raw-data/github-results.json"

    # 테스트 저장소 목록
    local repos=(
        "octocat/Hello-World"
        "facebook/react"
        "microsoft/vscode"
        "torvalds/linux"
    )

    echo "{" > "$output_file"
    echo '  "platform": "GitHub",' >> "$output_file"
    echo '  "timestamp": "'$(date -Iseconds)'",' >> "$output_file"
    echo '  "tests": [' >> "$output_file"

    local test_count=0

    for repo in "${repos[@]}"; do
        log_info "테스트 중: $repo"

        # 디렉토리 리스팅 테스트
        local start_time=$(date +%s.%N)
        local response=$(curl -s -H "Authorization: token $GITHUB_TOKEN" \
                             -w "%{http_code}" \
                             "https://api.github.com/repos/$repo/contents/")
        local end_time=$(date +%s.%N)
        local duration=$(echo "$end_time - $start_time" | bc)
        local http_code=${response: -3}

        [[ $test_count -gt 0 ]] && echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "repo": "$repo",
      "test": "directory-listing",
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))

        # 커밋 히스토리 테스트
        start_time=$(date +%s.%N)
        response=$(curl -s -H "Authorization: token $GITHUB_TOKEN" \
                       -w "%{http_code}" \
                       "https://api.github.com/repos/$repo/commits?per_page=50")
        end_time=$(date +%s.%N)
        duration=$(echo "$end_time - $start_time" | bc)
        http_code=${response: -3}

        echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "repo": "$repo",
      "test": "commit-history",
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))

        # Rate limit 준수
        sleep 1
    done

    echo "  ]" >> "$output_file"
    echo "}" >> "$output_file"

    log_success "GitHub 벤치마크 완료"
}

# GitLab 벤치마크
benchmark_gitlab() {
    log_info "GitLab CE 벤치마크 실행 중..."

    local output_file="$RESULTS_DIR/raw-data/gitlab-results.json"
    local project_id=1  # 테스트 프로젝트 ID

    echo "{" > "$output_file"
    echo '  "platform": "GitLab CE",' >> "$output_file"
    echo '  "timestamp": "'$(date -Iseconds)'",' >> "$output_file"
    echo '  "tests": [' >> "$output_file"

    local test_count=0

    # 페이지네이션 테스트
    for per_page in 20 50 100; do
        log_info "페이지네이션 테스트: per_page=$per_page"

        local start_time=$(date +%s.%N)
        local response=$(curl -s -H "Authorization: Bearer $GITLAB_TOKEN" \
                             -w "%{http_code}" \
                             "$GITLAB_URL/projects/$project_id/repository/tree?per_page=$per_page")
        local end_time=$(date +%s.%N)
        local duration=$(echo "$end_time - $start_time" | bc)
        local http_code=${response: -3}

        [[ $test_count -gt 0 ]] && echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "project_id": "$project_id",
      "test": "directory-listing",
      "per_page": $per_page,
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))
    done

    # 커밋 히스토리 테스트
    for path in "" "src/" "docs/"; do
        log_info "커밋 히스토리 테스트: path='$path'"

        local start_time=$(date +%s.%N)
        local response=$(curl -s -H "Authorization: Bearer $GITLAB_TOKEN" \
                             -w "%{http_code}" \
                             "$GITLAB_URL/projects/$project_id/repository/commits?path=$path&per_page=50")
        local end_time=$(date +%s.%N)
        local duration=$(echo "$end_time - $start_time" | bc)
        local http_code=${response: -3}

        echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "project_id": "$project_id",
      "test": "commit-history",
      "path": "$path",
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))
    done

    echo "  ]" >> "$output_file"
    echo "}" >> "$output_file"

    log_success "GitLab 벤치마크 완료"
}

# CollabOps 벤치마크
benchmark_collabops() {
    log_info "CollabOps 벤치마크 실행 중..."

    local output_file="$RESULTS_DIR/raw-data/collabops-results.json"

    echo "{" > "$output_file"
    echo '  "platform": "CollabOps",' >> "$output_file"
    echo '  "timestamp": "'$(date -Iseconds)'",' >> "$output_file"
    echo '  "tests": [' >> "$output_file"

    local test_count=0
    local repos=("small-repo" "medium-repo" "large-repo")

    for repo in "${repos[@]}"; do
        log_info "테스트 중: $repo"

        # 디렉토리 리스팅 테스트
        local start_time=$(date +%s.%N)
        local response=$(curl -s -w "%{http_code}" \
                             "$COLLABOPS_URL/repos/test-org/$repo/overview/tree/commits")
        local end_time=$(date +%s.%N)
        local duration=$(echo "$end_time - $start_time" | bc)
        local http_code=${response: -3}

        [[ $test_count -gt 0 ]] && echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "repo": "$repo",
      "test": "directory-listing",
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))

        # 커밋 히스토리 테스트
        start_time=$(date +%s.%N)
        response=$(curl -s -X POST \
                       -H "Content-Type: application/json" \
                       -d '{"paths": ["src/"], "ref": "main"}' \
                       -w "%{http_code}" \
                       "$COLLABOPS_URL/repos/test-org/$repo/overview/latest-commits")
        end_time=$(date +%s.%N)
        duration=$(echo "$end_time - $start_time" | bc)
        http_code=${response: -3}

        echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "repo": "$repo",
      "test": "commit-history",
      "duration": $duration,
      "http_code": $http_code,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))
    done

    echo "  ]" >> "$output_file"
    echo "}" >> "$output_file"

    log_success "CollabOps 벤치마크 완료"
}

# 동시 부하 테스트
concurrent_load_test() {
    log_info "동시 부하 테스트 실행 중..."

    local output_file="$RESULTS_DIR/raw-data/concurrent-load-results.json"
    local concurrency_levels=(1 5 10 20 50)

    echo "{" > "$output_file"
    echo '  "test": "concurrent-load",' >> "$output_file"
    echo '  "timestamp": "'$(date -Iseconds)'",' >> "$output_file"
    echo '  "results": [' >> "$output_file"

    local test_count=0

    for concurrency in "${concurrency_levels[@]}"; do
        log_info "동시 사용자 테스트: $concurrency 명"

        # CollabOps 동시 부하 테스트
        local temp_results=$(mktemp)

        seq 1 $concurrency | xargs -I {} -P $concurrency bash -c "
            start_time=\$(date +%s.%N)
            response=\$(curl -s -w '%{http_code}' '$COLLABOPS_URL/repos/test-org/medium-repo/overview/tree/commits')
            end_time=\$(date +%s.%N)
            duration=\$(echo \"\$end_time - \$start_time\" | bc)
            http_code=\${response: -3}
            echo \"duration:\$duration,code:\$http_code\"
        " > "$temp_results"

        # 결과 집계
        local success_count=0
        local total_duration=0
        local response_times=()

        while IFS= read -r line; do
            local duration=$(echo "$line" | cut -d',' -f1 | cut -d':' -f2)
            local code=$(echo "$line" | cut -d',' -f2 | cut -d':' -f2)

            response_times+=($duration)
            total_duration=$(echo "$total_duration + $duration" | bc)
            [[ "$code" == "200" ]] && ((success_count++))
        done < "$temp_results"

        local avg_duration=$(echo "scale=6; $total_duration / $concurrency" | bc)
        local success_rate=$(echo "scale=2; $success_count * 100 / $concurrency" | bc)

        # P95 계산 (간단한 근사치)
        IFS=$'\n' sorted=($(sort -n <<<"${response_times[*]}"))
        local p95_index=$(echo "($concurrency * 95 + 99) / 100" | bc)
        local p95_duration=${sorted[$p95_index]}

        [[ $test_count -gt 0 ]] && echo "    ," >> "$output_file"
        cat >> "$output_file" << EOF
    {
      "platform": "CollabOps",
      "concurrency": $concurrency,
      "avg_duration": $avg_duration,
      "p95_duration": $p95_duration,
      "success_rate": $success_rate,
      "success_count": $success_count,
      "total_requests": $concurrency,
      "timestamp": "$(date -Iseconds)"
    }
EOF
        ((test_count++))

        rm -f "$temp_results"
        sleep 5  # 시스템 복구 대기
    done

    echo "  ]" >> "$output_file"
    echo "}" >> "$output_file"

    log_success "동시 부하 테스트 완료"
}

# 결과 분석
analyze_results() {
    log_info "결과 분석 중..."

    local analysis_file="$RESULTS_DIR/analysis/summary.md"

    cat > "$analysis_file" << EOF
# 벤치마크 결과 분석

**실행 일시**: $(date -Iseconds)
**테스트 환경**: Docker Compose 로컬 환경

## 플랫폼별 성능 요약

EOF

    # 각 플랫폼의 평균 성능 계산
    for platform in github gitlab collabops; do
        if [[ -f "$RESULTS_DIR/raw-data/$platform-results.json" ]]; then
            echo "### $platform 성능" >> "$analysis_file"

            # JSON 파일에서 평균 응답시간 추출 (jq 사용)
            if command -v jq >/dev/null 2>&1; then
                local avg_time=$(jq -r '.tests[] | .duration' "$RESULTS_DIR/raw-data/$platform-results.json" | \
                                awk '{sum+=$1; count++} END {printf "%.3f", sum/count}')
                echo "- 평균 응답시간: ${avg_time}초" >> "$analysis_file"
            else
                echo "- jq가 설치되지 않아 상세 분석을 수행할 수 없습니다." >> "$analysis_file"
            fi

            echo "" >> "$analysis_file"
        fi
    done

    cat >> "$analysis_file" << EOF

## 원시 데이터 위치

- GitHub 결과: \`raw-data/github-results.json\`
- GitLab 결과: \`raw-data/gitlab-results.json\`
- CollabOps 결과: \`raw-data/collabops-results.json\`
- 동시 부하 결과: \`raw-data/concurrent-load-results.json\`

## 다음 단계

1. 결과 데이터를 엑셀이나 데이터 분석 도구로 가져오기
2. 플랫폼별 성능 차이 시각화
3. 개선 우선순위 결정
4. 최적화 전후 비교 분석
EOF

    log_success "분석 완료: $analysis_file"
}

# 메인 실행 함수
main() {
    log_info "CollabOps 통합 벤치마크 시작"
    log_info "=========================================="

    check_environment
    check_platform_health
    setup_results_directory

    # 개별 플랫폼 테스트 실행
    [[ -n "${GITHUB_TOKEN:-}" ]] && benchmark_github
    [[ -n "${GITLAB_TOKEN:-}" ]] && benchmark_gitlab
    benchmark_collabops

    # 동시 부하 테스트
    concurrent_load_test

    # 결과 분석
    analyze_results

    log_success "=========================================="
    log_success "벤치마크 완료! 결과: $RESULTS_DIR"
    log_info "분석 보고서: $RESULTS_DIR/analysis/summary.md"
}

# 스크립트 실행
main "$@"
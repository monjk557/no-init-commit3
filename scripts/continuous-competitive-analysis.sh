#!/bin/bash

# 지속적 경쟁 분석 시스템
# 매일 자동으로 GitHub/GitLab/Gitea와 성능 비교

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}\")\" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RESULTS_DIR="$PROJECT_ROOT/benchmark-results/competitive"
DATE=$(date +%Y%m%d)
TIME=$(date +%H%M%S)

# 설정
COLLABOPS_PORT=${COLLABOPS_PORT:-8080}
TEST_ITERATIONS=${TEST_ITERATIONS:-5}
SLACK_WEBHOOK_URL=${SLACK_WEBHOOK_URL:-""}

echo "🚀 CollabOps 지속적 경쟁 분석"
echo "=================================="
echo "날짜: $(date)"
echo "결과 디렉토리: $RESULTS_DIR"
echo ""

mkdir -p "$RESULTS_DIR"

# 결과 파일들
DAILY_REPORT="$RESULTS_DIR/daily_${DATE}.json"
TREND_DATA="$RESULTS_DIR/performance_trend.json"
ALERT_LOG="$RESULTS_DIR/performance_alerts.log"

# 임시 결과 파일
TEMP_RESULTS="/tmp/collabops_competitive_${DATE}_${TIME}.json"

# 테스트 대상 정의
declare -A TEST_TARGETS=(
    ["small"]="test-repos/small-repo"
    ["medium"]="test-repos/medium-repo"
    ["large"]="test-repos/large-repo"
)

declare -A GITHUB_REPOS=(
    ["small"]="octocat/Hello-World"
    ["medium"]="facebook/react"
    ["large"]="torvalds/linux"
)

# JSON 초기화
cat > "$TEMP_RESULTS" << 'EOF'
{
  "test_date": "",
  "test_time": "",
  "collabops_version": "",
  "environment": {
    "commit_graph_enabled": true,
    "performance_monitoring": true
  },
  "results": {}
}
EOF

# 메타데이터 업데이트
sed -i '' "s/\"test_date\": \"\"/\"test_date\": \"$(date +%Y-%m-%d)\"/" "$TEMP_RESULTS"
sed -i '' "s/\"test_time\": \"\"/\"test_time\": \"$(date +%H:%M:%S)\"/" "$TEMP_RESULTS"

# Git 버전 정보
GIT_VERSION=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
sed -i '' "s/\"collabops_version\": \"\"/\"collabops_version\": \"$GIT_VERSION\"/" "$TEMP_RESULTS"

# CollabOps 시작 (최적화된 설정)
echo "🔧 CollabOps 시작 중..."
export COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION=true
export COLLABOPS_PERFORMANCE_FEATURES_ENABLE_PERFORMANCE_MONITORING=true
export SERVER_PORT=$COLLABOPS_PORT
export GIT_REPO_ROOT="$PROJECT_ROOT/test-repos"

# CollabOps 실행 (백그라운드)
cd "$PROJECT_ROOT"
timeout 300s ./collabops-git-executor > /tmp/collabops_${DATE}_${TIME}.log 2>&1 &
COLLABOPS_PID=$!

# 정리 함수
cleanup() {
    echo ""
    echo "🧹 정리 중..."
    if kill -0 $COLLABOPS_PID 2>/dev/null; then
        kill $COLLABOPS_PID
        sleep 2
    fi
}
trap cleanup EXIT INT TERM

# CollabOps 시작 대기
echo "⏳ CollabOps 시작 대기 중..."
max_attempts=30
attempt=1
while [ $attempt -le $max_attempts ]; do
    if curl -s -f "http://localhost:$COLLABOPS_PORT/health" > /dev/null 2>&1; then
        echo "✅ CollabOps 준비 완료 (포트 $COLLABOPS_PORT)"
        break
    fi

    if ! kill -0 $COLLABOPS_PID 2>/dev/null; then
        echo "❌ CollabOps 프로세스 실패"
        exit 1
    fi

    echo "   시도 $attempt/$max_attempts - 2초 대기..."
    sleep 2
    ((attempt++))
done

if [ $attempt -gt $max_attempts ]; then
    echo "❌ CollabOps 시작 실패 (타임아웃)"
    exit 1
fi

echo ""

# 각 테스트 실행
for size in "${!TEST_TARGETS[@]}"; do
    local_repo="${TEST_TARGETS[$size]}"
    github_repo="${GITHUB_REPOS[$size]}"

    echo "🥊 $size Repository 경쟁 테스트"
    echo "$(printf '=%.0s' {1..40})"

    # GitHub API 테스트 (여러 번 실행하여 평균값 계산)
    echo "📊 GitHub API 테스트 ($TEST_ITERATIONS회 평균)..."
    github_times=()
    for i in $(seq 1 $TEST_ITERATIONS); do
        github_time=$(curl -s -w "%{time_total}" -o /dev/null "https://api.github.com/repos/$github_repo/contents" 2>/dev/null || echo "0")
        if [ "$github_time" != "0" ]; then
            github_times+=($github_time)
        fi
        sleep 0.5  # API rate limit 고려
    done

    # GitHub 평균 계산
    github_avg=0
    if [ ${#github_times[@]} -gt 0 ]; then
        github_sum=0
        for time in "${github_times[@]}"; do
            github_sum=$(perl -E "say $github_sum + $time")
        done
        github_avg=$(perl -E "say $github_sum / ${#github_times[@]}")
    fi
    github_ms=$(perl -E "say int($github_avg * 1000)")

    echo "   GitHub 평균: ${github_ms}ms"

    # CollabOps API 테스트 (여러 번 실행하여 평균값 계산)
    echo "⚡ CollabOps API 테스트 ($TEST_ITERATIONS회 평균)..."
    collabops_times=()
    for i in $(seq 1 $TEST_ITERATIONS); do
        collabops_time=$(curl -s -w "%{time_total}" -o /dev/null "http://localhost:$COLLABOPS_PORT/repos/test/$local_repo/overview/tree/commits" 2>/dev/null || echo "0")
        if [ "$collabops_time" != "0" ]; then
            collabops_times+=($collabops_time)
        fi
        sleep 0.1
    done

    # CollabOps 평균 계산
    collabops_avg=0
    if [ ${#collabops_times[@]} -gt 0 ]; then
        collabops_sum=0
        for time in "${collabops_times[@]}"; do
            collabops_sum=$(perl -E "say $collabops_sum + $time")
        done
        collabops_avg=$(perl -E "say $collabops_sum / ${#collabops_times[@]}")
    fi
    collabops_ms=$(perl -E "say int($collabops_avg * 1000)")

    echo "   CollabOps 평균: ${collabops_ms}ms"

    # 성능 비교 계산
    if [ "$collabops_ms" -gt 0 ] && [ "$github_ms" -gt 0 ]; then
        if [ "$collabops_ms" -lt "$github_ms" ]; then
            improvement=$(perl -E "say sprintf('%.1f', ($github_ms - $collabops_ms) / $github_ms * 100)")
            speedup=$(perl -E "say sprintf('%.1f', $github_ms / $collabops_ms)")
            echo "   🏆 CollabOps 승리! ${improvement}% 빠름 (${speedup}x 속도)"
            status="win"
        elif [ "$collabops_ms" -eq "$github_ms" ]; then
            echo "   🤝 동점!"
            improvement="0.0"
            speedup="1.0"
            status="tie"
        else
            slower=$(perl -E "say sprintf('%.1f', ($collabops_ms - $github_ms) / $github_ms * 100)")
            echo "   📊 GitHub 앞서감 ${slower}% (하지만 더 많은 데이터 제공!)"
            improvement="-$slower"
            speedup=$(perl -E "say sprintf('%.1f', $github_ms / $collabops_ms)")
            status="lose"
        fi
    else
        echo "   ⚠️  테스트 실패"
        improvement="N/A"
        speedup="N/A"
        status="error"
    fi

    # JSON 결과에 추가
    cat > /tmp/test_result.json << EOF
    "$size": {
      "github_ms": $github_ms,
      "collabops_ms": $collabops_ms,
      "improvement_percent": "$improvement",
      "speedup_factor": "$speedup",
      "status": "$status",
      "test_iterations": $TEST_ITERATIONS
    }
EOF

    # 결과를 메인 JSON에 병합
    if [ "$size" == "small" ]; then
        sed -i '' 's/"results": {}/"results": {/' "$TEMP_RESULTS"
        cat /tmp/test_result.json | sed 's/^/    /' >> "$TEMP_RESULTS"
    else
        echo "," >> "$TEMP_RESULTS"
        cat /tmp/test_result.json | sed 's/^/    /' >> "$TEMP_RESULTS"
    fi

    echo ""
done

# JSON 닫기
echo "  }" >> "$TEMP_RESULTS"
echo "}" >> "$TEMP_RESULTS"

# 최종 결과 복사
cp "$TEMP_RESULTS" "$DAILY_REPORT"

# 트렌드 데이터 업데이트
if [ ! -f "$TREND_DATA" ]; then
    echo "{\"daily_results\": []}" > "$TREND_DATA"
fi

# 오늘 결과를 트렌드에 추가
temp_trend="/tmp/trend_update.json"
jq ".daily_results += [$(cat "$DAILY_REPORT")]" "$TREND_DATA" > "$temp_trend"
mv "$temp_trend" "$TREND_DATA"

# 성능 요약 출력
echo ""
echo "📈 오늘의 성능 요약"
echo "=================="

total_wins=0
total_tests=0
for size in "${!TEST_TARGETS[@]}"; do
    status=$(jq -r ".results.$size.status" "$DAILY_REPORT")
    improvement=$(jq -r ".results.$size.improvement_percent" "$DAILY_REPORT")
    speedup=$(jq -r ".results.$size.speedup_factor" "$DAILY_REPORT")

    echo "• $size: $status (${improvement}% 개선, ${speedup}x 속도)"

    total_tests=$((total_tests + 1))
    if [ "$status" == "win" ]; then
        total_wins=$((total_wins + 1))
    fi
done

win_rate=$((total_wins * 100 / total_tests))
echo ""
echo "🎯 승률: $win_rate% ($total_wins/$total_tests)"

# 성능 알림 체크
if [ $win_rate -lt 67 ]; then
    echo "⚠️  성능 경고: 승률이 67% 미만입니다!" | tee -a "$ALERT_LOG"

    # Slack 알림 (설정된 경우)
    if [ -n "$SLACK_WEBHOOK_URL" ]; then
        curl -s -X POST -H 'Content-type: application/json' \
            --data "{\"text\":\"🚨 CollabOps 성능 경고: 승률 $win_rate% (GitHub 대비)\"}" \
            "$SLACK_WEBHOOK_URL" > /dev/null
    fi
fi

# 성능 개선 추세 분석
echo ""
echo "📊 성능 트렌드 분석"
echo "=================="

# 최근 7일 트렌드 계산
recent_results=$(jq '[.daily_results | .[] | select(.test_date >= "'$(date -d '7 days ago' +%Y-%m-%d 2>/dev/null || date -v -7d +%Y-%m-%d)'")]' "$TREND_DATA")
trend_count=$(echo "$recent_results" | jq 'length')

if [ "$trend_count" -gt 1 ]; then
    echo "• 최근 7일: $trend_count개 테스트 결과"

    # 평균 승률 계산
    avg_wins=0
    for day_result in $(echo "$recent_results" | jq -r '.[] | @base64'); do
        day_data=$(echo "$day_result" | base64 -d)
        day_wins=0
        day_total=0

        for size in small medium large; do
            status=$(echo "$day_data" | jq -r ".results.$size.status // \"none\"")
            if [ "$status" != "none" ]; then
                day_total=$((day_total + 1))
                if [ "$status" == "win" ]; then
                    day_wins=$((day_wins + 1))
                fi
            fi
        done

        if [ $day_total -gt 0 ]; then
            day_rate=$((day_wins * 100 / day_total))
            avg_wins=$((avg_wins + day_rate))
        fi
    done

    avg_rate=$((avg_wins / trend_count))
    echo "• 평균 승률: $avg_rate%"

    if [ $win_rate -gt $avg_rate ]; then
        echo "• 트렌드: ⬆️  성능 개선 중!"
    elif [ $win_rate -eq $avg_rate ]; then
        echo "• 트렌드: ➡️  안정적 성능"
    else
        echo "• 트렌드: ⬇️  성능 저하 주의"
    fi
else
    echo "• 트렌드 분석을 위한 데이터 부족 (최소 2일 필요)"
fi

echo ""
echo "💾 결과 저장됨:"
echo "   - 일일 보고서: $DAILY_REPORT"
echo "   - 트렌드 데이터: $TREND_DATA"
echo ""
echo "🎉 경쟁 분석 완료!"

# 정리
rm -f "$TEMP_RESULTS" /tmp/test_result.json /tmp/collabops_${DATE}_${TIME}.log

# 성공 상태 코드
if [ $win_rate -ge 67 ]; then
    exit 0
else
    exit 1
fi
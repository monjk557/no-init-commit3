#!/usr/bin/env bash
# guard-git-native — executor 도메인 가드: git/ssh substrate 를 native 로 다룬다.
#
# #6272 big-rule-1 ("git 을 gogit 으로 한겹더 X → native git") + GO-STANDARD §4/§7.
# executor 는 git 을 서브프로세스로 직접 부르고, ssh(git-receive-pack)·http 가 그 계층을
# 공유한다. 세 불변식을 강제한다:
#   1. go-git import 래칫 — native git 이행이 되돌아가지 않게(신규 go-git 의존 금지)
#   2. os.Chdir 금지 — 프로세스 전역 cwd 는 동시 push(ssh/http)를 손상. --git-dir/cmd.Dir 사용
#   3. 비-Context exec.Command 래칫 — git 서브프로세스는 취소·타임아웃 가능해야(CommandContext)
#
# ratchet: baseline 은 현재 잔존치. 초과 시 fail(신규 유입 차단), 감소 시 baseline 하향 권장.
set -eu

fail=0

# 1) go-git import 래칫 (native git 이행 유지)
BASE_GOGIT=16
cur=$(grep -rl 'github.com/go-git/go-git' --include='*.go' internal/ cmd/ 2>/dev/null | grep -v _test | wc -l | tr -d ' ')
if [ "$cur" -gt "$BASE_GOGIT" ]; then
  echo "✗ go-git 파일 $BASE_GOGIT → $cur (+$((cur - BASE_GOGIT))): native git 이행 역행 — 새 go-git 의존 금지."
  echo "  git 은 서브프로세스로 직접 부른다(gogit 래퍼 확장 금지). 남은 go-git 은 축소만."
  fail=1
else
  echo "✓ go-git import $cur ≤ $BASE_GOGIT (native git 이행 유지)"
  [ "$cur" -lt "$BASE_GOGIT" ] && echo "  개선: BASE_GOGIT 을 $cur 로 낮추세요."
fi

# 2) os.Chdir 금지 (프로세스 전역 cwd — 동시 git over ssh/http 손상)
n=$(grep -rn 'os\.Chdir' --include='*.go' internal/ cmd/ 2>/dev/null | grep -v _test | wc -l | tr -d ' ')
if [ "$n" -ne 0 ]; then
  echo "✗ os.Chdir $n 건 — 프로세스 전역 cwd 는 동시 push 를 손상한다. git --git-dir / cmd.Dir 사용."
  grep -rn 'os\.Chdir' --include='*.go' internal/ cmd/ 2>/dev/null | grep -v _test | sed 's/^/    /'
  fail=1
else
  echo "✓ os.Chdir 0 (동시 git cwd 안전)"
fi

# 3) 비-Context exec.Command 래칫 (git 서브프로세스 취소가능성 — GO-STANDARD §4)
BASE_EXEC=1
n=$(grep -rn 'exec\.Command(' --include='*.go' internal/ 2>/dev/null | grep -v _test | grep -v 'CommandContext' | wc -l | tr -d ' ')
if [ "$n" -gt "$BASE_EXEC" ]; then
  echo "✗ 비-Context exec.Command $BASE_EXEC → $n: git 서브프로세스는 exec.CommandContext(취소·타임아웃)로."
  grep -rn 'exec\.Command(' --include='*.go' internal/ 2>/dev/null | grep -v _test | grep -v 'CommandContext' | sed 's/^/    /'
  fail=1
else
  echo "✓ 비-Context exec.Command $n ≤ $BASE_EXEC (git subprocess 취소가능)"
  [ "$n" -lt "$BASE_EXEC" ] && echo "  개선: BASE_EXEC 를 $n 로 낮추세요."
fi

[ "$fail" -eq 0 ] && echo "✅ guard-git-native 통과" || echo "❌ guard-git-native 실패"
exit $fail

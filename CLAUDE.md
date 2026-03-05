# Claude Instructions for CollabOps Git Executor

## Development Philosophy

### Long-Term Solutions Only
When analyzing issues or proposing solutions for this codebase, **ALWAYS** focus exclusively on long-term, architectural solutions. Never suggest short-term improvements, quick fixes, or band-aid solutions.

**Rationale**: 
- Short-term fixes create technical debt and mask underlying architectural problems
- This codebase serves as critical infrastructure that must scale and evolve
- Band-aid solutions compound into maintenance nightmares
- Long-term thinking prevents recurring issues and builds robust systems

### Solution Characteristics

**What to ALWAYS provide:**
- ✅ Architectural redesigns that address root causes
- ✅ Scalable patterns that grow with system demands
- ✅ Self-healing and adaptive mechanisms
- ✅ Multi-phase implementation strategies (months/quarters)
- ✅ Comprehensive observability and metrics
- ✅ Resource coordination and intelligent management
- ✅ Future-proof designs that anticipate growth

**What to NEVER suggest:**
- ❌ Quick configuration changes
- ❌ Simple parameter adjustments
- ❌ Temporary workarounds
- ❌ "Try this for now" solutions
- ❌ Manual intervention procedures
- ❌ Point fixes without systemic thinking

### Analysis Approach

1. **Root Cause Analysis**: Always dig to fundamental architectural assumptions and design flaws
2. **System Thinking**: Consider how issues relate to overall system architecture
3. **Scalability Focus**: Ensure solutions work at 10x current scale
4. **Intelligence Integration**: Propose adaptive, learning systems where appropriate
5. **Resource Management**: Consider system-wide resource coordination

### Implementation Strategy

Every solution should include:
- Multi-phase timeline (typically 3-6 months)
- Dependencies and prerequisites
- Success metrics and observability
- Migration strategy from current state
- Risk mitigation approaches

## Code Quality Standards

### MANDATORY: Quality Checks After Every Code Change

**CRITICAL REQUIREMENT**: After ANY code modification (Edit, Write, or creation), you MUST immediately run:

```bash
# 1. Format code first
make format

# 2. Run go vet check
go vet ./...

# 3. Fix any errors before proceeding
```

**Workflow for EVERY code change:**

1. **Make code change** (Edit/Write file)
2. **IMMEDIATELY run `make format`** - fixes formatting issues
3. **IMMEDIATELY run `go vet ./...`** - catches compilation errors, unused variables, etc.
4. **If errors exist**: Fix them immediately, don't proceed to next change
5. **If clean**: Continue with next change or commit

**Before ANY commit:**
```bash
# Final verification
make format
go vet ./...
go test ./internal/merge
```

**Never ignore or defer go vet errors** - they indicate real issues that can cause:
- Compilation failures in CI
- Runtime bugs
- Unused variables masking real problems
- Import cycles
- Dead code

### Quality Gates

- ✅ **`go vet ./...` must pass** - zero errors allowed
- ✅ **`make format` must run clean** - consistent formatting
- ✅ **Core tests must pass** - `go test ./internal/merge`
- ✅ **No unused imports or variables** - clean code only

**This is non-negotiable for production-quality code.**

### Automated Quality Enforcement

**Integration with Development Workflow:**

Every response that includes code changes should follow this pattern:

1. **Make the code change**
2. **Auto-run quality checks:**
   ```bash
   make format && go vet ./...
   ```
3. **Report status**: "✅ Code quality checks passed" or "🔧 Fixed formatting/vet issues"
4. **If issues found**: Fix immediately and re-run checks
5. **Only then proceed** to next change or completion

**Error Response Template:**
When go vet fails, immediately:
```bash
# Fix the specific issues
# Re-run: make format && go vet ./...
# Verify: echo "✅ All quality checks now pass"
```

**Commit Readiness Checklist:**
```bash
make format              # ✅ Formatting standardized
go vet ./...            # ✅ No vet errors
go test ./internal/merge # ✅ Core functionality works
git status              # ✅ Ready to commit
```

## Examples

**Bad (Short-term):**
"Increase the timeout from 30s to 300s"

**Good (Long-term):**  
"Implement an adaptive timeout management system that scales based on repository size, network conditions, and system load, with predictive capabilities and self-healing mechanisms"

**Bad (Short-term):**
"Add retry logic for failed operations"

**Good (Long-term):**
"Design a distributed Git protocol optimization with chunked transfer, resume capability, and intelligent resource coordination across the entire system"

## Worktree Workflow

워크트리에서 작업 후 정리할 때 **반드시 아래 순서를 지킬 것**:

1. 워크트리 안에서 커밋 완료
2. **메인 레포 경로로 먼저 이동** (`cd /Users/seungbaek/Desktop/CODE/collabops-git-executor`)
3. 그 다음 머지 + 워크트리 제거 + 브랜치 삭제

```bash
# 반드시 메인 레포로 이동한 뒤 실행
cd /Users/seungbaek/Desktop/CODE/collabops-git-executor
git merge <worktree-branch> --no-edit
git worktree remove .claude/worktrees/<name>
git branch -d <worktree-branch>
```

**절대 하지 말 것**: 워크트리 디렉토리 안에서 워크트리 제거/머지 시도 — CWD가 삭제된 경로를 가리키게 되어 이후 모든 git/파일 명령이 실패함.

---

*Remember: We build systems that last, scale, and evolve. Every solution should make the system more intelligent, more adaptive, and more capable of handling future challenges.*
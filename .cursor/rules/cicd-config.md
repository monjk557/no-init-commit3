# CI/CD Configuration Protection Rules

This rule protects project-specific CI/CD configurations from being incorrectly modified by AI assistants.

## 🚨 CRITICAL: Never Change These Without Verification

### GCP Artifact Registry Settings
```yaml
# collabops-git-executor specific settings - DO NOT CHANGE
env:
  REGION: asia-northeast3         # 한국 리전
  REPOSITORY: git-executor        # ⚠️ SPECIFIC to this project
  IMAGE: collabops-git-executor   # ⚠️ SPECIFIC to this project
```

**NEVER change to generic names like:**
- ❌ `REPOSITORY: web`
- ❌ `REPOSITORY: backend`
- ❌ `REPOSITORY: api`
- ❌ `IMAGE: app`

### Project-Specific Identifiers

**These are INTENTIONALLY different across CollabOps projects:**

| Project | REPOSITORY | IMAGE |
|---------|------------|-------|
| collabops-git-executor | git-executor | collabops-git-executor |
| collabops-api-orchestrator | api-orchestrator | collabops-api-orchestrator |
| collabops-web | web | collabops-web |

## 📋 Before Modifying CI/CD Files

**ASK YOURSELF:**
1. Is this a project-specific identifier? → DON'T CHANGE IT
2. Is this referencing another project's setup? → ADAPT, DON'T COPY
3. Does this match the project name? → KEEP IT THAT WAY

## 🎯 Common Mistakes to Avoid

### 1. Copying Settings from Other Projects
```yaml
# ❌ WRONG: Copying from collabops-web
REPOSITORY: web  # This is for collabops-web, NOT git-executor!

# ✅ CORRECT: Keep project-specific name
REPOSITORY: git-executor
```

### 2. Using "Proven Working" Settings from Other Projects
Just because it works in another project doesn't mean it's correct here:
- Each project has its own GCP Artifact Registry repository
- Repository names are intentionally different
- Don't assume one project's settings apply to another

### 3. Generalizing Names
```yaml
# ❌ WRONG: Making it "generic"
IMAGE: backend-service

# ✅ CORRECT: Keep specific name
IMAGE: collabops-git-executor
```

## 🔍 How to Verify Settings

**If unsure about a setting:**
1. Check the project name in `go.mod`
2. Look at existing Docker images in GCP
3. Compare with the repository name on GitHub
4. Ask for clarification rather than guessing

## 📝 Safe Changes vs Unsafe Changes

### ✅ SAFE to Change:
- Version numbers
- Timeout values
- Cache settings
- Build arguments (that aren't identifiers)
- Step names and descriptions
- Docker build optimization
- Health check intervals

### ❌ UNSAFE to Change Without Verification:
- Repository names
- Image names
- Project IDs
- Service account names
- Registry URLs
- Region settings (without checking requirements)
- File paths in Docker COPY commands (verify files exist!)

## 🤖 AI Assistant Guidelines

**When working with CI/CD files:**

1. **PRESERVE** project-specific identifiers
2. **ADAPT** logic but not names when copying from other projects
3. **VERIFY** before changing any GCP-related settings
4. **ASK** if a setting seems generic but might be intentional

**Example scenario:**
```
User: "The CI is failing, I saw collabops-api uses 'web' repository"

❌ WRONG RESPONSE: "Let me change REPOSITORY to 'web' to match the working setup"

✅ CORRECT RESPONSE: "Each CollabOps project has its own repository.
   git-executor uses 'git-executor', not 'web'. Let's investigate
   the actual error instead of changing project-specific settings."
```

## 🐳 Docker Configuration

### File Existence Verification
**Always verify files exist before adding COPY commands:**
```dockerfile
# ❌ WRONG: Copying non-existent file
COPY --from=builder /app/config/config.yaml /config/config.yaml.default

# ✅ CORRECT: Only copy files that exist
COPY --from=builder /app/config/config.yaml.example /config/config.yaml.example
```

### Application Defaults
**Leverage application's built-in defaults:**
```dockerfile
# ❌ WRONG: Hardcoding config paths that may not exist
CMD ["--config", "/config/config.yaml"]

# ✅ CORRECT: Let app use defaults if designed to do so
CMD []
```

## 🚀 Remember

**Project-specific !== Broken**

Just because a setting is different from another project doesn't mean it's wrong. Each project in the CollabOps ecosystem has its own:
- GCP Artifact Registry repository
- Docker image names
- Deployment configurations
- File structures and dependencies

These differences are **intentional** and **required**.

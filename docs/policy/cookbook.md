# Policy Configuration Cookbook

## 🍳 Real-World Policy Recipes

Practical policy configurations for large-scale go-git platforms (1000+ developers).

## 🌟 Essential Recipes

### 1. Basic Environment Protection

**Use Case**: Protect environment files across all repositories

```yaml
version: "1.0"
metadata:
  name: "Basic Environment Protection"

policies:
  - name: "env-protection"
    enabled: true
    type: "file_protection"
    scope:
      branches: ["main", "master", "production"]
    rules:
      - name: "env-files"
        patterns:
          - "*.env"
          - ".env*"
          - "**/*.env"
        action: "block_deletion"
    enforcement: "strict"
```

**Result**: Prevents accidental deletion of environment files in main branches.

### 2. Microservice Architecture Protection

**Use Case**: Different policies for different service types

```yaml
policies:
  # Critical user-facing services
  - name: "critical-services"
    priority: 1000
    scope:
      repositories: ["user-service", "payment-*", "auth-*", "*-api"]
      branches: ["main", "production"]
    rules:
      - name: "production-configs"
        patterns:
          - "config/production/**/*"
          - "*.prod.*"
          - "environments/prod/**/*"
        action: "block_deletion"

  # Internal tools - more relaxed
  - name: "internal-tools"
    priority: 100
    scope:
      repositories: ["tool-*", "*-cli", "admin-*"]
    rules:
      - name: "config-guidelines"
        patterns: ["config/**/*"]
        action: "warn"
```

**Result**: Critical services have strict protection, internal tools have guidelines only.

### 3. Team-Based Repository Access

**Use Case**: Different teams have different permission levels

```yaml
policies:
  - name: "infrastructure-team-repos"
    scope:
      repositories: ["infrastructure-*", "*-terraform", "k8s-*"]
    rules:
      - name: "infra-files"
        patterns:
          - "**/*.tf"
          - "**/*.tfvars"
          - "k8s/**/*"
        action: "require_approval"
        metadata:
          approvers: ["infrastructure-team"]

overrides:
  # Infrastructure team can modify their own repos
  - policy_name: "infrastructure-team-repos"
    scope:
      teams: ["infrastructure-team"]
    action: "warn"  # Reduce to warning for the owning team
```

**Result**: Infrastructure files require approval from infrastructure team, except for the team members themselves.

### 4. Staging vs Production Separation

**Use Case**: Different protection levels for different environments

```yaml
policies:
  # Production - strict protection
  - name: "production-protection"
    priority: 1000
    scope:
      branches: ["main", "production", "release/*"]
    rules:
      - patterns: ["**/production/**/*", "*.prod.*"]
        action: "block_deletion"

  # Staging - require approval
  - name: "staging-protection"
    priority: 500
    scope:
      branches: ["develop", "staging"]
    rules:
      - patterns: ["**/staging/**/*", "*.staging.*"]
        action: "require_approval"

  # Development - guidelines only
  - name: "development-guidelines"
    priority: 100
    scope:
      branches: ["feature/*", "dev/*"]
    rules:
      - patterns: ["**/dev/**/*", "*.dev.*"]
        action: "warn"
```

**Result**: Graduated protection levels based on environment criticality.

## 🏗️ Infrastructure Recipes

### 5. Kubernetes Manifest Protection

**Use Case**: Protect Kubernetes deployment files

```yaml
policies:
  - name: "k8s-manifest-protection"
    scope:
      repositories: ["*"]  # All repos can have k8s files
    rules:
      # Production k8s files - block deletion
      - name: "k8s-production"
        patterns:
          - "k8s/production/**/*.yaml"
          - "k8s/production/**/*.yml"
          - "manifests/production/**/*"
        action: "block_deletion"
        scope:
          branches: ["main", "production"]

      # Staging k8s files - require approval
      - name: "k8s-staging"
        patterns:
          - "k8s/staging/**/*"
          - "manifests/staging/**/*"
        action: "require_approval"

      # All other k8s files - warn
      - name: "k8s-general"
        patterns:
          - "k8s/**/*.yaml"
          - "k8s/**/*.yml"
          - "**/*.k8s.yaml"
        action: "warn"
        metadata:
          message: "K8s manifest changed - verify with platform team"
```

### 6. Docker Configuration Protection

**Use Case**: Protect Docker and container configurations

```yaml
policies:
  - name: "docker-protection"
    rules:
      - name: "production-docker"
        patterns:
          - "Dockerfile.prod"
          - "docker-compose.prod.yml"
          - "docker/production/**/*"
        action: "require_approval"
        scope:
          branches: ["main", "production"]

      - name: "docker-general"
        patterns:
          - "Dockerfile*"
          - "docker-compose*.yml"
          - ".dockerignore"
        action: "warn"
```

### 7. Database Migration Protection

**Use Case**: Protect database schema changes

```yaml
policies:
  - name: "database-protection"
    scope:
      repositories: ["*-service", "*-api"]  # Services with databases
    rules:
      - name: "production-migrations"
        patterns:
          - "migrations/**/*.sql"
          - "db/migrations/**/*"
          - "schema/**/*.sql"
        action: "require_approval"
        scope:
          branches: ["main", "production"]
        metadata:
          approvers: ["dba-team", "backend-team"]

      - name: "migration-guidelines"
        patterns:
          - "migrations/**/*"
          - "db/**/*"
        action: "warn"
        metadata:
          message: "Database changes detected - ensure backward compatibility"
```

## 🔐 Security Recipes

### 8. Security-Sensitive Files

**Use Case**: Protect certificates, keys, and security configurations

```yaml
policies:
  - name: "security-files"
    priority: 2000  # Highest priority
    scope:
      repositories: ["*"]
    rules:
      - name: "certificates-and-keys"
        patterns:
          - "**/*.pem"
          - "**/*.key"
          - "**/*.p12"
          - "**/*.jks"
          - "certs/**/*"
          - "keys/**/*"
          - "ssl/**/*"
        action: "block_deletion"
        enforcement: "strict"

      - name: "security-configs"
        patterns:
          - "**/security/**/*"
          - "**/*security*.yml"
          - "auth/**/*"
          - "oauth/**/*"
        action: "require_approval"
        metadata:
          approvers: ["security-team"]
```

### 9. Secrets Management

**Use Case**: Prevent accidental exposure of secrets

```yaml
policies:
  - name: "secrets-protection"
    rules:
      - name: "secret-files"
        patterns:
          - "**/secrets/**/*"
          - "**/*.secret*"
          - "**/*password*"
          - "**/*token*"
          - ".env.local"  # Often contains local secrets
        action: "block_deletion"

      - name: "potential-secrets"
        patterns:
          - "**/*api*key*"
          - "**/*private*"
          - "config/local/**/*"
        action: "warn"
        metadata:
          message: "Potential secret detected - verify no credentials are committed"
```

## 🚀 CI/CD Recipes

### 10. CI/CD Pipeline Protection

**Use Case**: Protect CI/CD configurations and deployment scripts

```yaml
policies:
  - name: "cicd-protection"
    rules:
      - name: "github-actions"
        patterns:
          - ".github/workflows/**/*"
          - ".github/actions/**/*"
        action: "require_approval"
        scope:
          branches: ["main"]
        metadata:
          approvers: ["devops-team"]

      - name: "deployment-scripts"
        patterns:
          - "scripts/deploy/**/*"
          - "deploy/**/*"
          - "**/*deploy*.sh"
          - "**/*release*.sh"
        action: "require_approval"

      - name: "ci-configs"
        patterns:
          - ".gitlab-ci.yml"
          - ".circleci/**/*"
          - "Jenkinsfile*"
          - "buildspec.yml"
        action: "warn"
```

## 👥 Large Team Recipes

### 11. Frontend Team Specific

**Use Case**: Frontend-specific file protections

```yaml
policies:
  - name: "frontend-protection"
    scope:
      repositories: ["*-frontend", "*-web", "*-ui"]
    rules:
      - name: "build-configs"
        patterns:
          - "webpack*.js"
          - "vite.config.*"
          - "next.config.js"
          - "package.json"
          - "yarn.lock"
          - "package-lock.json"
        action: "warn"
        scope:
          branches: ["main", "production"]

      - name: "env-files"
        patterns:
          - ".env*"
          - "public/**/*.env*"
        action: "block_deletion"
```

### 12. Backend Team Specific

**Use Case**: Backend service protections

```yaml
policies:
  - name: "backend-protection"
    scope:
      repositories: ["*-service", "*-api", "*-backend"]
    rules:
      - name: "go-modules"
        patterns:
          - "go.mod"
          - "go.sum"
        action: "warn"
        scope:
          branches: ["main"]

      - name: "service-configs"
        patterns:
          - "config/**/*.yml"
          - "internal/config/**/*"
        action: "require_approval"
        scope:
          branches: ["main", "production"]
```

### 13. Data Team Specific

**Use Case**: Data pipeline and analytics protections

```yaml
policies:
  - name: "data-protection"
    scope:
      repositories: ["*-data", "*-analytics", "*-etl", "*-pipeline"]
    rules:
      - name: "data-pipelines"
        patterns:
          - "dags/**/*"  # Airflow DAGs
          - "pipelines/**/*"
          - "**/*.sql"
          - "notebooks/**/*.ipynb"
        action: "require_approval"
        metadata:
          approvers: ["data-team", "analytics-team"]

      - name: "data-configs"
        patterns:
          - "config/datasources/**/*"
          - "**/database.yml"
          - "**/*warehouse*.yml"
        action: "block_deletion"
```

## 🛠️ Utility Recipes

### 14. Emergency Override Template

**Use Case**: Temporary policy bypass for emergencies

```yaml
# Use with caution - for emergency situations only
overrides:
  - policy_name: "*"  # All policies
    scope:
      users: ["emergency-admin"]
      branches: ["hotfix/*", "emergency/*"]
    enabled: false  # Disable all policies
    metadata:
      reason: "Emergency override - requires post-incident review"
      expires_at: "2026-02-26T00:00:00Z"  # Auto-expire
```

### 15. Gradual Rollout Template

**Use Case**: Gradually introduce new policies

```yaml
policies:
  - name: "new-policy-pilot"
    enabled: true
    scope:
      # Start with specific teams/repos
      teams: ["platform-team"]
      repositories: ["pilot-*", "test-*"]
    rules:
      - name: "new-protection"
        patterns: ["**/*.new-extension"]
        action: "warn"  # Start with warnings

# After validation, expand scope:
# scope:
#   repositories: ["*"]  # All repos
# action: "block_deletion"  # Strengthen enforcement
```

## 📊 Monitoring Recipe

### 16. Policy Health Check

**Use Case**: Monitor policy engine health and violations

```yaml
# Add to your monitoring configuration
policies:
  - name: "monitoring-self-check"
    scope:
      repositories: ["monitoring-*", "observability-*"]
    rules:
      - name: "monitoring-configs"
        patterns:
          - "prometheus/**/*"
          - "grafana/**/*"
          - "alerts/**/*"
        action: "require_approval"
        metadata:
          approvers: ["sre-team"]
          reason: "Monitoring changes affect system observability"
```

## 💡 Best Practice Tips

### Pattern Design Tips

```yaml
# ✅ Good patterns - specific and clear
patterns:
  - "config/production/*.env"    # Specific path and extension
  - "k8s/production/**/*.yaml"   # Specific directory structure
  - "*.prod.json"               # Clear naming convention

# ❌ Avoid - too broad or unclear
patterns:
  - "**/*"                      # Matches everything
  - "*prod*"                    # Too vague, matches many unrelated files
  - "config/**/*"               # Too broad without file type
```

### Enforcement Strategy

```yaml
# Graduated enforcement approach
enforcement_levels:
  critical: "block_deletion"      # Production secrets, security files
  important: "require_approval"   # Infrastructure, staging configs
  guidelines: "warn"              # Development files, documentation
```

---

**💡 Pro Tip**: Start with basic environment protection and gradually add more specific policies based on actual team needs and incident learnings.

**🔄 Regular Review**: Review policy violations monthly and adjust patterns based on false positives and missed violations.
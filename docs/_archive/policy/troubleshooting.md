# Policy Engine Troubleshooting Guide

## 🔧 Common Issues & Solutions

Troubleshooting guide for large-scale Policy Engine deployments (1000+ developers).

## 🚨 Emergency Procedures

### Immediate Policy Bypass (Emergency Only)

```bash
# 1. Disable policy engine completely
export COLLABOPS_POLICY_ENGINE_ENABLED=false

# 2. Or disable via API (if available)
curl -X POST "localhost:8080/api/policy/emergency-disable" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"reason": "INCIDENT-12345", "duration": "1h"}'

# 3. Restart service to apply changes
sudo systemctl restart collabops-git-executor
```

**⚠️ Remember**: Document emergency bypasses for post-incident review.

## 📊 Diagnostic Commands

### Quick Health Check

```bash
# Check policy engine status
curl -s "localhost:8080/api/policy/status" | jq

# Expected output:
{
  "initialized": true,
  "config_source": "file:///etc/collabops/policies/environment-protection.yml",
  "policies_loaded": 5,
  "rules_loaded": 15,
  "version": "1.0",
  "last_reload": "2026-02-25T10:00:00Z"
}
```

### Check Specific File Protection

```bash
# Test file protection for specific file
curl -s "localhost:8080/api/policy/evaluate?file=config/production.env" | jq

# Sample output:
{
  "protected": true,
  "reason": "file matches protected pattern '*.env' in policy 'env-protection'",
  "rule_matched": {
    "name": "env-files",
    "type": "file_protection",
    "action": "block_deletion"
  }
}
```

### Log Analysis

```bash
# Check policy engine logs
tail -f /var/log/collabops/policy-engine.log | grep -E "(ERROR|WARN|policy)"

# Look for these patterns:
# - "PolicyEngine initialization failed"
# - "Policy validation failed"
# - "Hot reload failed"
# - "Pattern compilation failed"
```

## 🔍 Common Problems

### 1. Policy Engine Not Starting

#### Symptoms
- Service starts but policy engine shows as disabled
- Logs show "PolicyEngine not initialized"
- All merge operations proceed without policy checks

#### Diagnosis
```bash
# Check environment variable
echo $COLLABOPS_POLICY_ENGINE_ENABLED

# Check policy file location
ls -la /etc/collabops/policies/
ls -la /configs/policies/

# Check file permissions
stat /etc/collabops/policies/environment-protection.yml
```

#### Solutions
```bash
# 1. Enable policy engine
export COLLABOPS_POLICY_ENGINE_ENABLED=true

# 2. Create policy file if missing
mkdir -p /etc/collabops/policies
cat > /etc/collabops/policies/environment-protection.yml << EOF
version: "1.0"
metadata:
  name: "Basic Protection"
policies:
  - name: "env-protection"
    enabled: true
    type: "file_protection"
    rules:
      - name: "env-files"
        patterns: ["*.env"]
        action: "block_deletion"
    enforcement: "strict"
EOF

# 3. Fix permissions
chown collabops:collabops /etc/collabops/policies/*.yml
chmod 644 /etc/collabops/policies/*.yml

# 4. Restart service
sudo systemctl restart collabops-git-executor
```

### 2. Policy Validation Failures

#### Symptoms
- Logs show "Policy validation failed"
- Service starts but policies not loaded
- API returns validation errors

#### Common Validation Errors

```yaml
# ❌ Missing required metadata
version: "1.0"
# Missing metadata section

# ✅ Fixed
version: "1.0"
metadata:
  name: "Policy Name"
  description: "Policy description"
```

```yaml
# ❌ Invalid pattern (too broad)
rules:
  - patterns: ["**/*"]  # Matches everything, rejected

# ✅ Fixed
rules:
  - patterns: ["*.env", "config/**/*.yml"]  # Specific patterns
```

```yaml
# ❌ Invalid action
rules:
  - action: "delete"  # Invalid action

# ✅ Fixed
rules:
  - action: "block_deletion"  # Valid action
```

#### Validation Commands
```bash
# Validate policy file manually
curl -X POST "localhost:8080/api/policy/validate" \
  -H "Content-Type: application/yaml" \
  --data-binary @/etc/collabops/policies/environment-protection.yml
```

### 3. Files Not Protected as Expected

#### Symptoms
- Important files being deleted despite policies
- Policies seem to be ignored
- No blocking rules triggered

#### Diagnosis Steps

**Step 1: Check Pattern Matching**
```bash
# Test specific file pattern
curl -s "localhost:8080/api/policy/debug/pattern" \
  -d '{"file": "config/production.env", "pattern": "*.env"}' | jq
```

**Step 2: Check Policy Scope**
```yaml
# Verify policy scope includes your branch/repo
policies:
  - name: "env-protection"
    scope:
      branches: ["main", "production"]  # Must match your branch
      repositories: ["*"]               # Must match your repo
```

**Step 3: Check Rule Priority**
```bash
# List policies by priority
curl -s "localhost:8080/api/policy/hierarchy" | jq '.layer_info[] | {name, priority, enabled}'
```

#### Solutions
```yaml
# 1. Fix pattern matching
rules:
  - patterns:
    # Instead of: "config/*.env" (only matches direct children)
    # Use: "config/**/*.env" (matches all subdirectories)
    - "config/**/*.env"
    - "**/*.env.production"

# 2. Broaden scope
scope:
  branches: ["*"]  # All branches instead of specific ones
  repositories: ["*"]  # All repos

# 3. Check enforcement level
enforcement: "strict"  # Not "warn" or "disabled"
```

### 4. Performance Issues

#### Symptoms
- Merge operations taking too long
- High CPU usage
- Policy evaluation timeouts

#### Performance Diagnosis

```bash
# Check evaluation metrics
curl -s "localhost:8080/metrics" | grep policy_evaluation

# Look for:
# policy_evaluation_duration_seconds - should be < 100ms
# policy_evaluations_total - rate of evaluations
# policy_cache_hit_ratio - should be > 0.8
```

#### Performance Solutions

```yaml
# 1. Optimize patterns
rules:
  - patterns:
    # ✅ Efficient - specific extensions
    - "*.env"
    - "config/**/*.yml"

    # ❌ Inefficient - too many wildcards
    - "**/*env*/**/*.*"

# 2. Reduce policy count
# Instead of 50 small policies, create 10 well-organized policies

# 3. Increase cache settings
engine_config:
  pattern_cache_size: 10000
  evaluation_timeout: "200ms"
```

### 5. Hot Reload Issues

#### Symptoms
- Policy changes not applied automatically
- Manual restart required after policy updates
- Hot reload errors in logs

#### Hot Reload Diagnosis
```bash
# Check hot reload status
curl -s "localhost:8080/api/policy/reload/status" | jq

# Check file watcher
lsof | grep /etc/collabops/policies

# Check file permissions for watcher
ls -la /etc/collabops/policies/
```

#### Solutions
```bash
# 1. Restart hot reload
curl -X POST "localhost:8080/api/policy/reload/restart"

# 2. Manual reload
curl -X POST "localhost:8080/api/policy/reload"

# 3. Check file system events
# Some container environments don't support file watching
# Use polling mode instead:
export COLLABOPS_POLICY_RELOAD_MODE=polling
```

## 🎯 Large Scale Issues (1000+ Developers)

### High Volume Problems

#### Problem: Too Many Policy Evaluations
```bash
# Symptoms: High evaluation rate
curl -s localhost:8080/metrics | grep policy_evaluations_total
# Shows > 1000/sec

# Solution: Implement caching
cache_config:
  evaluation_cache_ttl: "30s"
  max_cache_entries: 100000
```

#### Problem: Repository-Specific Overload
```yaml
# Problem: Some repos have too many policy violations
# Check top violating repos
curl -s "localhost:8080/api/policy/metrics/violations" | jq

# Solution: Repository-specific policies
policies:
  - name: "high-volume-repo-optimization"
    scope:
      repositories: ["high-traffic-repo"]
    rules:
      - patterns: ["*.env"]
        action: "warn"  # Reduce to warning instead of blocking
```

### Team-Specific Issues

#### Problem: Team Conflicts Over Policies
```yaml
# Solution: Team-based overrides
overrides:
  - policy_name: "strict-env-protection"
    scope:
      teams: ["frontend-team"]
    enforcement: "warn"  # Frontend team gets warnings only

  - policy_name: "strict-env-protection"
    scope:
      teams: ["backend-team"]
    enforcement: "strict"  # Backend team gets strict enforcement
```

#### Problem: Emergency Access for Oncall
```yaml
# Solution: Emergency override policy
policies:
  - name: "emergency-access"
    priority: 2000  # Highest priority
    scope:
      branches: ["hotfix/*", "emergency/*"]
      teams: ["oncall-team"]
    rules:
      - patterns: ["*"]
        action: "warn"  # Allow all changes with warnings
```

## 🔧 Advanced Debugging

### Debug Mode

```bash
# Enable debug logging
export COLLABOPS_LOG_LEVEL=debug

# Debug specific file evaluation
curl -s "localhost:8080/api/policy/debug/evaluate" \
  -d '{
    "file": "config/production.env",
    "branch": "main",
    "repository": "user-service",
    "verbose": true
  }' | jq
```

### Pattern Testing Tool

```bash
# Test pattern matching
for file in config/prod.env config/staging.env test.env; do
  echo "Testing: $file"
  curl -s "localhost:8080/api/policy/debug/pattern" \
    -d "{\"file\": \"$file\", \"pattern\": \"*.env\"}" | jq .matches
done
```

### Policy Simulation

```bash
# Simulate policy changes before applying
curl -X POST "localhost:8080/api/policy/simulate" \
  -H "Content-Type: application/yaml" \
  --data-binary @new-policy.yml | jq
```

## 📞 Getting Help

### Log Collection

```bash
# Collect diagnostic information
cat > collect-logs.sh << 'EOF'
#!/bin/bash
echo "=== Policy Engine Diagnostics ==="
echo "Date: $(date)"
echo

echo "=== Environment ==="
env | grep COLLABOPS
echo

echo "=== Policy Status ==="
curl -s localhost:8080/api/policy/status | jq
echo

echo "=== Recent Logs ==="
tail -100 /var/log/collabops/policy-engine.log
echo

echo "=== Policy Files ==="
find /etc/collabops/policies -name "*.yml" -exec echo "File: {}" \; -exec head -20 {} \;
EOF

chmod +x collect-logs.sh
./collect-logs.sh > policy-diagnostics.txt
```

### Support Escalation

1. **Level 1**: Check this troubleshooting guide
2. **Level 2**: Contact Platform Team with diagnostics
3. **Level 3**: Emergency bypass procedures for production issues

### Common Error Messages

| Error | Meaning | Solution |
|-------|---------|----------|
| `policy_engine_not_initialized` | Engine failed to start | Check config file and permissions |
| `pattern compilation failed` | Invalid regex in pattern | Fix pattern syntax |
| `policy validation failed` | Invalid YAML structure | Use validation API to check |
| `file watcher error` | Hot reload not working | Check file permissions or use polling |
| `evaluation timeout` | Policy check taking too long | Optimize patterns or increase timeout |

---

**🚨 Remember**: When in doubt, you can always temporarily disable the policy engine for emergency situations, but document all bypasses for post-incident review.

**📧 Support**: platform-team@company.com
**Emergency**: oncall-platform@company.com
**Documentation**: https://internal-docs.company.com/policy-engine
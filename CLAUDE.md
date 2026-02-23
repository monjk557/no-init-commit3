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

## Examples

**Bad (Short-term):**
"Increase the timeout from 30s to 300s"

**Good (Long-term):**  
"Implement an adaptive timeout management system that scales based on repository size, network conditions, and system load, with predictive capabilities and self-healing mechanisms"

**Bad (Short-term):**
"Add retry logic for failed operations"

**Good (Long-term):**
"Design a distributed Git protocol optimization with chunked transfer, resume capability, and intelligent resource coordination across the entire system"

---

*Remember: We build systems that last, scale, and evolve. Every solution should make the system more intelligent, more adaptive, and more capable of handling future challenges.*
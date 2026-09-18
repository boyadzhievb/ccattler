export const meta = {
  name: 'pre-commit-validation',
  description: 'Run code review, architecture guard, and tests on staged changes before committing',
  phases: [
    { title: 'Validate', detail: 'parallel code review + architecture check + tests' },
    { title: 'Verdict', detail: 'synthesize results into go/no-go decision' },
  ],
}

const REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['APPROVE', 'REQUEST_CHANGES', 'NEEDS_DISCUSSION'] },
    bugs: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          severity: { type: 'string', enum: ['critical', 'high', 'medium', 'low'] },
          description: { type: 'string' },
        },
        required: ['file', 'severity', 'description'],
      },
    },
    styleViolations: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          rule: { type: 'string' },
          description: { type: 'string' },
        },
        required: ['file', 'rule', 'description'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['verdict', 'bugs', 'styleViolations', 'summary'],
}

const ARCH_SCHEMA = {
  type: 'object',
  properties: {
    clean: { type: 'boolean' },
    violations: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          rule: { type: 'string' },
          file: { type: 'string' },
          description: { type: 'string' },
        },
        required: ['rule', 'file', 'description'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['clean', 'violations', 'summary'],
}

const TEST_SCHEMA = {
  type: 'object',
  properties: {
    passed: { type: 'integer' },
    failed: { type: 'integer' },
    allPassing: { type: 'boolean' },
    failures: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          test: { type: 'string' },
          package: { type: 'string' },
          reason: { type: 'string' },
        },
        required: ['test', 'package', 'reason'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['passed', 'failed', 'allPassing', 'failures', 'summary'],
}

const QUALITY_SCHEMA = {
  type: 'object',
  properties: {
    vetClean: { type: 'boolean' },
    issues: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          category: { type: 'string' },
          description: { type: 'string' },
        },
        required: ['file', 'category', 'description'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['vetClean', 'issues', 'summary'],
}

// Phase 1: Run all checks in parallel
phase('Validate')
log('Running 4 parallel validation checks on current changes')

const results = await parallel([
  () => agent(
    'Review the current git diff in CCattler for correctness bugs and style violations. ' +
    'Run: cd /Users/boyadboz/REPOS/ccattler && git diff && git diff --cached\n' +
    'Check for: logic errors, nil pointer dereferences, race conditions, resource leaks, error handling. ' +
    'Check style: doc comments on every function, descriptive variable names (4+ chars, no abbreviations), ' +
    'receiver names matching type, documented struct fields. ' +
    'Read changed files for full context before reporting.',
    { label: 'code-reviewer', phase: 'Validate', schema: REVIEW_SCHEMA }
  ),
  () => agent(
    'Run architecture checks on CCattler. Focus on recent changes (git diff). ' +
    'Check: no cross-controller imports, desired/observed state separation, ' +
    'idempotent operations (ensure_X not do_X), declarative facts not commands, ' +
    'controllers return []Change not direct mutations, no layer violations ' +
    '(CLI/API importing controller internals, controllers importing runtime). ' +
    'Run grep checks: cd /Users/boyadboz/REPOS/ccattler && ' +
    'for dir in controllers/*/; do name=$(basename "$dir"); grep -rn "controllers/" "$dir" --include="*.go" | grep -v "_test.go" | grep -v "controllers/$name"; done',
    { label: 'architecture-guard', phase: 'Validate', schema: ARCH_SCHEMA }
  ),
  () => agent(
    'Run the full CCattler test suite. ' +
    'Run: cd /Users/boyadboz/REPOS/ccattler && go test -count=1 -race -timeout 120s ./... ' +
    'Report pass/fail counts and details on any failures.',
    { label: 'test-runner', phase: 'Validate', schema: TEST_SCHEMA }
  ),
  () => agent(
    'Run Go static analysis on CCattler. ' +
    'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go build ./... ' +
    'Check for: compilation errors, vet warnings, build failures. ' +
    'Also scan recently changed files (git diff --name-only) for missing function doc comments ' +
    'and short variable names that violate project rules.',
    { label: 'code-quality', phase: 'Validate', schema: QUALITY_SCHEMA }
  ),
])

const codeReview = results[0]
const archCheck = results[1]
const testResult = results[2]
const qualityCheck = results[3]

// Phase 2: Synthesize verdict
phase('Verdict')

const blockers = []

if (codeReview && codeReview.bugs.some(function(b) { return b.severity === 'critical' || b.severity === 'high' })) {
  blockers.push('Code review found critical/high bugs: ' + codeReview.bugs.filter(function(b) { return b.severity === 'critical' || b.severity === 'high' }).length)
}
if (archCheck && !archCheck.clean) {
  blockers.push('Architecture violations: ' + archCheck.violations.length)
}
if (testResult && testResult.failed > 0) {
  blockers.push('Test failures: ' + testResult.failed)
}
if (qualityCheck && !qualityCheck.vetClean) {
  blockers.push('Go vet/build issues: ' + qualityCheck.issues.length)
}

const commitAllowed = blockers.length === 0

if (commitAllowed) {
  log('All checks passed — safe to commit')
} else {
  log('BLOCKED — ' + blockers.join('; '))
}

return {
  commitAllowed: commitAllowed,
  blockers: blockers,
  codeReview: codeReview ? {
    verdict: codeReview.verdict,
    bugCount: codeReview.bugs.length,
    styleViolations: codeReview.styleViolations.length,
  } : null,
  architecture: archCheck ? {
    clean: archCheck.clean,
    violationCount: archCheck.violations.length,
  } : null,
  tests: testResult ? {
    passed: testResult.passed,
    failed: testResult.failed,
  } : null,
  quality: qualityCheck ? {
    vetClean: qualityCheck.vetClean,
    issueCount: qualityCheck.issues.length,
  } : null,
}

export const meta = {
  name: 'feature-development',
  description: 'Implement a feature, review it, test it, and update docs — full pipeline',
  phases: [
    { title: 'Implement', detail: 'lead-developer writes code and tests' },
    { title: 'Review', detail: 'code-reviewer + architecture-guard check the diff' },
    { title: 'Test', detail: 'test-runner validates everything passes' },
    { title: 'Document', detail: 'update STATUS.md with completion' },
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
          fix: { type: 'string' },
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
          line: { type: 'integer' },
          description: { type: 'string' },
          fix: { type: 'string' },
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
    skipped: { type: 'integer' },
    failures: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          test: { type: 'string' },
          package: { type: 'string' },
          reason: { type: 'string' },
          suggestedFix: { type: 'string' },
        },
        required: ['test', 'package', 'reason'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['passed', 'failed', 'failures', 'summary'],
}

const featureDescription = args || 'Implement the next item from the current milestone'

// Phase 1: Implement
phase('Implement')
log('Lead developer implementing: ' + featureDescription)

const implementation = await agent(
  `You are the lead developer for CCattler. Implement the following feature:\n\n${featureDescription}\n\n` +
  'Read CLAUDE.md for architectural rules and code style. Read STATUS.md for current project state. ' +
  'Implement the feature with tests. Follow all code style rules (doc comments, descriptive names, receiver names). ' +
  'After implementing, run go vet ./... and go test ./... to verify. ' +
  'Report what files you changed, what tests you added, and whether tests pass.',
  { label: 'lead-developer', phase: 'Implement', model: 'opus' }
)

log('Implementation complete, starting review')

// Phase 2: Review (parallel — code review + architecture guard)
phase('Review')

const reviews = await parallel([
  () => agent(
    'Review the current git diff (unstaged + staged changes) in the CCattler project. ' +
    'Check for: correctness bugs, code style violations (CLAUDE.md rules: doc comments on every function, ' +
    'descriptive variable names 4+ chars, receiver names matching type, documented struct fields), ' +
    'and architectural drift (controller isolation, state separation, idempotency). ' +
    'Run: cd /Users/boyadboz/REPOS/ccattler && git diff\n' +
    'Read changed files for full context. Report structured findings.',
    { label: 'code-reviewer', phase: 'Review', schema: REVIEW_SCHEMA }
  ),
  () => agent(
    'Run architecture guard checks on the current changes in CCattler. ' +
    'Check the 7 architectural rules: controller isolation (no cross-controller imports), ' +
    'state separation (desired/ vs observed/), idempotency (ensure_X not do_X), ' +
    'declarative facts (no command facts), proposed changes (Reconcile returns []Change), ' +
    'layer separation (CLI/API dont import controller internals), ' +
    'receiver names (match the type, not abbreviated). ' +
    'Run the grep checks from .claude/agents/architecture-guard.md. Report violations.',
    { label: 'architecture-guard', phase: 'Review', schema: ARCH_SCHEMA }
  ),
])

const codeReview = reviews[0]
const archReview = reviews[1]

const hasBugs = codeReview && codeReview.bugs.length > 0 && codeReview.bugs.some(function(b) { return b.severity === 'critical' || b.severity === 'high' })
const hasArchViolations = archReview && !archReview.clean

if (hasBugs) {
  log('Code review found critical/high bugs — these need fixing before merge')
}
if (hasArchViolations) {
  log('Architecture violations found — these need fixing before merge')
}
if (!hasBugs && !hasArchViolations) {
  log('Review passed, running tests')
}

// Phase 3: Test
phase('Test')

const testResult = await agent(
  'Run the full CCattler test suite and report results. ' +
  'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go test -count=1 -race -timeout 120s ./... ' +
  'For any failures, read the failing test and the code under test to identify the cause. ' +
  'Report: number passed, failed, skipped, and detailed analysis of each failure.',
  { label: 'test-runner', phase: 'Test', schema: TEST_SCHEMA }
)

if (testResult && testResult.failed > 0) {
  log('Tests failed: ' + testResult.failed + ' failures detected')
} else {
  log('All tests passed')
}

// Phase 4: Document
phase('Document')

await agent(
  'Update STATUS.md in the CCattler project to reflect that work was just completed. ' +
  'Read the current STATUS.md, then update it: move any completed items to Recent Completions with today\'s date, ' +
  'update the Current Milestone section if needed, and update Metrics if the file/line counts changed. ' +
  'Run: wc -l $(find . -name "*.go" ! -name "*_test.go") | tail -1 to get current line count. ' +
  'Run: find . -name "*.go" ! -name "*_test.go" | wc -l for source file count. ' +
  'Run: find . -name "*_test.go" | wc -l for test file count.',
  { label: 'status-updater', phase: 'Document' }
)

// Final summary
return {
  feature: featureDescription,
  review: codeReview ? {
    verdict: codeReview.verdict,
    bugCount: codeReview.bugs.length,
    styleViolationCount: codeReview.styleViolations.length,
  } : null,
  architecture: archReview ? {
    clean: archReview.clean,
    violationCount: archReview.violations.length,
  } : null,
  tests: testResult ? {
    passed: testResult.passed,
    failed: testResult.failed,
  } : null,
}

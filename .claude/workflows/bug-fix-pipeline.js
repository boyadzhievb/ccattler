export const meta = {
  name: 'bug-fix-pipeline',
  description: 'Diagnose a bug, write fix with regression test, review, and validate',
  phases: [
    { title: 'Diagnose', detail: 'lead-developer reproduces and identifies root cause' },
    { title: 'Review', detail: 'code-reviewer checks fix for correctness and side effects' },
    { title: 'Test', detail: 'test-runner confirms fix and no regressions' },
  ],
}

var FIX_SCHEMA = {
  type: 'object',
  properties: {
    rootCause: { type: 'string' },
    filesChanged: {
      type: 'array',
      items: { type: 'string' },
    },
    testsAdded: {
      type: 'array',
      items: { type: 'string' },
    },
    testsPass: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['rootCause', 'filesChanged', 'testsAdded', 'testsPass', 'summary'],
}

var REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['APPROVE', 'REQUEST_CHANGES', 'NEEDS_DISCUSSION'] },
    correctness: { type: 'boolean' },
    sideEffects: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          description: { type: 'string' },
          severity: { type: 'string', enum: ['critical', 'warning', 'info'] },
        },
        required: ['description', 'severity'],
      },
    },
    regressionTestAdequate: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['verdict', 'correctness', 'sideEffects', 'regressionTestAdequate', 'summary'],
}

var TEST_SCHEMA = {
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
  required: ['passed', 'failed', 'allPassing', 'summary'],
}

var bugDescription = args || 'Investigate the most recent test failure or reported bug'

// Phase 1: Diagnose and fix
phase('Diagnose')
log('Diagnosing: ' + bugDescription)

var fixResult = await agent(
  'You are the lead developer for CCattler. Diagnose and fix this bug:\n\n' + bugDescription + '\n\n' +
  'Steps:\n' +
  '1. Read CLAUDE.md for architectural context and code style rules.\n' +
  '2. Reproduce the bug — run relevant tests or read the failing code path.\n' +
  '3. Identify the root cause — read the affected code, trace the logic.\n' +
  '4. Write a failing test that captures the bug BEFORE fixing it.\n' +
  '5. Fix the code so the test passes.\n' +
  '6. Run go test ./... to verify no regressions.\n' +
  '7. Follow all CLAUDE.md style rules (doc comments, descriptive names, receiver names).\n\n' +
  'Report: root cause, files changed, tests added, whether tests pass.',
  { label: 'lead-developer', phase: 'Diagnose', model: 'opus', schema: FIX_SCHEMA }
)

if (!fixResult) {
  log('Diagnosis failed — no result from lead-developer')
  return { success: false, reason: 'diagnosis failed' }
}

log('Root cause: ' + fixResult.rootCause)
log('Changed ' + fixResult.filesChanged.length + ' files, added ' + fixResult.testsAdded.length + ' tests')

// Phase 2: Review
phase('Review')
log('Reviewing fix for correctness and side effects')

var reviewResult = await agent(
  'Review the current git diff in CCattler. A bug fix was just applied.\n' +
  'Bug: ' + bugDescription + '\n' +
  'Root cause: ' + fixResult.rootCause + '\n' +
  'Files changed: ' + fixResult.filesChanged.join(', ') + '\n' +
  'Tests added: ' + fixResult.testsAdded.join(', ') + '\n\n' +
  'Run: cd /Users/boyadboz/REPOS/ccattler && git diff\n' +
  'Check:\n' +
  '1. Does the fix actually address the root cause, or just the symptom?\n' +
  '2. Are there any side effects — other code paths that could break?\n' +
  '3. Is the regression test adequate — does it actually catch the original bug?\n' +
  '4. Does the fix follow CLAUDE.md style rules?\n' +
  '5. Does the fix preserve architectural invariants?',
  { label: 'code-reviewer', phase: 'Review', schema: REVIEW_SCHEMA }
)

if (reviewResult && reviewResult.verdict === 'REQUEST_CHANGES') {
  log('Review requests changes: ' + reviewResult.summary)
} else if (reviewResult && reviewResult.verdict === 'APPROVE') {
  log('Review approved')
}

// Phase 3: Full test suite
phase('Test')
log('Running full test suite to confirm no regressions')

var testResult = await agent(
  'Run the full CCattler test suite after a bug fix. ' +
  'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go test -count=1 -race -timeout 120s ./... ' +
  'Report pass/fail counts. For any failures, read the failing test to determine if it is a regression caused by the fix.',
  { label: 'test-runner', phase: 'Test', schema: TEST_SCHEMA }
)

var allGreen = testResult && testResult.allPassing

if (allGreen) {
  log('All tests pass — fix is complete')
} else {
  log('Tests failing after fix — ' + (testResult ? testResult.failed : 'unknown') + ' failures')
}

return {
  success: allGreen && reviewResult && reviewResult.verdict === 'APPROVE',
  bug: bugDescription,
  rootCause: fixResult.rootCause,
  filesChanged: fixResult.filesChanged,
  testsAdded: fixResult.testsAdded,
  review: reviewResult ? {
    verdict: reviewResult.verdict,
    sideEffectCount: reviewResult.sideEffects.length,
    regressionTestAdequate: reviewResult.regressionTestAdequate,
  } : null,
  tests: testResult ? {
    passed: testResult.passed,
    failed: testResult.failed,
    allPassing: testResult.allPassing,
  } : null,
}

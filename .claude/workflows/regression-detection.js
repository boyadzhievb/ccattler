export const meta = {
  name: 'regression-detection',
  description: 'Detect test regression, bisect to find breaking commit, fix and verify',
  phases: [
    { title: 'Detect', detail: 'identify failing tests and bisect to breaking commit' },
    { title: 'Fix', detail: 'lead-developer analyzes and fixes the regression' },
    { title: 'Verify', detail: 'code-reviewer reviews, test-runner confirms all green' },
  ],
}

var DETECT_SCHEMA = {
  type: 'object',
  properties: {
    failingTests: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          test: { type: 'string' },
          package: { type: 'string' },
          errorMessage: { type: 'string' },
        },
        required: ['test', 'package', 'errorMessage'],
      },
    },
    breakingCommit: {
      type: 'object',
      properties: {
        hash: { type: 'string' },
        message: { type: 'string' },
        author: { type: 'string' },
        filesChanged: {
          type: 'array',
          items: { type: 'string' },
        },
      },
      required: ['hash', 'message'],
    },
    bisectSuccessful: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['failingTests', 'bisectSuccessful', 'summary'],
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
  required: ['rootCause', 'filesChanged', 'testsPass', 'summary'],
}

var REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['APPROVE', 'REQUEST_CHANGES'] },
    correctness: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['verdict', 'correctness', 'summary'],
}

var TEST_SCHEMA = {
  type: 'object',
  properties: {
    passed: { type: 'integer' },
    failed: { type: 'integer' },
    allPassing: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['passed', 'failed', 'allPassing', 'summary'],
}

var failingTest = args || null

// Phase 1: Detect
phase('Detect')

var detectPrompt = 'Detect test regressions in the CCattler project.\n'
if (failingTest) {
  detectPrompt += 'Known failing test: ' + failingTest + '\n'
}
detectPrompt +=
  'Steps:\n' +
  '1. Run the test suite: cd /Users/boyadboz/REPOS/ccattler && go test -count=1 -timeout 120s ./... 2>&1\n' +
  '2. Identify any failing tests.\n' +
  '3. For each failing test, try to bisect to the breaking commit:\n' +
  '   - Run: git log --oneline -20 to see recent commits\n' +
  '   - For the failing package, test at HEAD~1, HEAD~2, etc. until it passes:\n' +
  '     git stash && git checkout HEAD~N && go test -run TestName ./package/ && git checkout - && git stash pop\n' +
  '   - Report which commit introduced the failure.\n' +
  '   - If bisect takes more than 5 steps, stop and report the range.\n' +
  '4. Read the breaking commit diff to understand what changed: git show <hash>\n' +
  'IMPORTANT: Return to the original branch/commit when done. Do not leave the repo in a detached HEAD state.'

log('Detecting regressions' + (failingTest ? ' (known: ' + failingTest + ')' : ''))

var detectResult = await agent(detectPrompt, { label: 'regression-detector', phase: 'Detect', schema: DETECT_SCHEMA })

if (!detectResult || detectResult.failingTests.length === 0) {
  log('No failing tests found — no regression to fix')
  return { regressionFound: false, allPassing: true }
}

log('Found ' + detectResult.failingTests.length + ' failing test(s)')
if (detectResult.bisectSuccessful && detectResult.breakingCommit) {
  log('Breaking commit: ' + detectResult.breakingCommit.hash + ' — ' + detectResult.breakingCommit.message)
}

// Phase 2: Fix
phase('Fix')

var fixContext = 'Fix a test regression in CCattler.\n\n'
fixContext += 'Failing tests:\n'
detectResult.failingTests.forEach(function(test) {
  fixContext += '- ' + test.package + '/' + test.test + ': ' + test.errorMessage + '\n'
})
if (detectResult.breakingCommit) {
  fixContext += '\nBreaking commit: ' + detectResult.breakingCommit.hash + ' — ' + detectResult.breakingCommit.message + '\n'
  if (detectResult.breakingCommit.filesChanged) {
    fixContext += 'Files changed in breaking commit: ' + detectResult.breakingCommit.filesChanged.join(', ') + '\n'
  }
}
fixContext +=
  '\nSteps:\n' +
  '1. Read the failing test(s) to understand what they verify.\n' +
  '2. Read the breaking commit diff (git show ' + (detectResult.breakingCommit ? detectResult.breakingCommit.hash : 'HEAD') + ') to understand the change.\n' +
  '3. Identify why the change broke the test — is it a real bug in the change, or does the test need updating?\n' +
  '4. Fix the code (preferred) or update the test if the new behavior is intentionally correct.\n' +
  '5. Add a regression test if the fix is in the code.\n' +
  '6. Run go test ./... to verify all tests pass.\n' +
  '7. Follow all CLAUDE.md style rules.'

log('Lead developer fixing regression')

var fixResult = await agent(fixContext, { label: 'lead-developer', phase: 'Fix', model: 'opus', schema: FIX_SCHEMA })

if (!fixResult) {
  log('Fix failed — no result from lead-developer')
  return { regressionFound: true, fixed: false, reason: 'fix attempt failed' }
}

log('Fix applied: ' + fixResult.rootCause)

// Phase 3: Verify (parallel — review + test)
phase('Verify')
log('Verifying fix with code review and full test suite')

var verifyResults = await parallel([
  function() {
    return agent(
      'Review the current git diff in CCattler. A regression fix was just applied.\n' +
      'Root cause: ' + fixResult.rootCause + '\n' +
      'Files changed: ' + fixResult.filesChanged.join(', ') + '\n' +
      'Run: cd /Users/boyadboz/REPOS/ccattler && git diff\n' +
      'Check: does the fix address the root cause? Any side effects? Style rules followed?',
      { label: 'code-reviewer', phase: 'Verify', schema: REVIEW_SCHEMA }
    )
  },
  function() {
    return agent(
      'Run the full CCattler test suite to verify a regression fix.\n' +
      'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go test -count=1 -race -timeout 120s ./...\n' +
      'Report pass/fail counts. Every test must pass.',
      { label: 'test-runner', phase: 'Verify', schema: TEST_SCHEMA }
    )
  },
])

var reviewResult = verifyResults[0]
var testResult = verifyResults[1]

var success = (testResult && testResult.allPassing) && (reviewResult && reviewResult.verdict === 'APPROVE')

if (success) {
  log('Regression fixed and verified — all tests pass, review approved')
} else {
  if (testResult && !testResult.allPassing) log('Tests still failing: ' + testResult.failed)
  if (reviewResult && reviewResult.verdict !== 'APPROVE') log('Review: ' + reviewResult.summary)
}

return {
  regressionFound: true,
  fixed: success,
  failingTests: detectResult.failingTests.map(function(t) { return t.package + '/' + t.test }),
  breakingCommit: detectResult.breakingCommit ? detectResult.breakingCommit.hash : null,
  rootCause: fixResult.rootCause,
  filesChanged: fixResult.filesChanged,
  review: reviewResult ? reviewResult.verdict : null,
  testsPassing: testResult ? testResult.allPassing : false,
}

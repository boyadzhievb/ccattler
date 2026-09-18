export const meta = {
  name: 'tech-debt-sprint',
  description: 'Scan for tech debt — agents verify mechanical findings, judgment calls surfaced to user',
  phases: [
    { title: 'Scan', detail: 'find dead code, duplication, complexity, style violations' },
    { title: 'Verify', detail: 'agents grep-verify mechanical findings (dead code, style)' },
    { title: 'Baseline', detail: 'confirm tests pass before any changes' },
  ],
}

var SCAN_SCHEMA = {
  type: 'object',
  properties: {
    mechanical: {
      type: 'object',
      properties: {
        deadCode: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              file: { type: 'string' },
              line: { type: 'integer' },
              name: { type: 'string' },
              kind: { type: 'string', enum: ['function', 'constant', 'variable', 'type'] },
              grepHits: { type: 'integer' },
            },
            required: ['file', 'name', 'kind', 'grepHits'],
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
              current: { type: 'string' },
              shouldBe: { type: 'string' },
            },
            required: ['file', 'rule', 'current', 'shouldBe'],
          },
        },
      },
      required: ['deadCode', 'styleViolations'],
    },
    judgmentNeeded: {
      type: 'object',
      properties: {
        duplication: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              pattern: { type: 'string' },
              locations: { type: 'array', items: { type: 'string' } },
              lineCount: { type: 'integer' },
              suggestion: { type: 'string' },
            },
            required: ['pattern', 'locations', 'suggestion'],
          },
        },
        complexity: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              file: { type: 'string' },
              function: { type: 'string' },
              lineCount: { type: 'integer' },
              nestingDepth: { type: 'integer' },
              suggestion: { type: 'string' },
            },
            required: ['file', 'function', 'lineCount', 'suggestion'],
          },
        },
      },
      required: ['duplication', 'complexity'],
    },
    summary: { type: 'string' },
  },
  required: ['mechanical', 'judgmentNeeded', 'summary'],
}

var VERIFY_SCHEMA = {
  type: 'object',
  properties: {
    confirmed: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          finding: { type: 'string' },
          grepCommand: { type: 'string' },
          grepResult: { type: 'string' },
          verified: { type: 'boolean' },
        },
        required: ['finding', 'grepCommand', 'verified'],
      },
    },
    falsePositives: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          finding: { type: 'string' },
          reason: { type: 'string' },
        },
        required: ['finding', 'reason'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['confirmed', 'falsePositives', 'summary'],
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

var focusArea = args || null

// Phase 1: Scan — find candidates, split into mechanical vs judgment
phase('Scan')
log('Scanning for tech debt' + (focusArea ? ' in ' + focusArea : ' across codebase'))

var scanPrompt = 'Scan the CCattler codebase for tech debt. ' +
  'Split your findings into TWO categories:\n\n' +
  'MECHANICAL (grep-verifiable, agents can confirm):\n' +
  '1. Dead code — unexported functions/types/constants with zero references.\n' +
  '   For EACH candidate, run: grep -rn "symbolName" --include="*.go" . | grep -v "func symbolName\\|type symbolName"\n' +
  '   Report the exact grep hit count. Only include if hits == 0.\n' +
  '2. Style violations — short receiver names (1-3 chars), missing doc comments.\n' +
  '   Run: grep -rn "func ([a-z]\\{1,3\\} \\*" --include="*.go" . | grep -v vendor | grep -v _test.go\n' +
  '   These are binary: either the comment exists or it does not.\n\n' +
  'JUDGMENT NEEDED (surfaced to user, NOT auto-verified):\n' +
  '3. Duplication — similar patterns across files. Report locations and line counts but do NOT judge whether extraction is worthwhile.\n' +
  '4. Complexity — functions over 90 lines. Report line count and nesting depth but do NOT suggest specific refactors.\n\n'

if (focusArea) {
  scanPrompt += 'Focus on: ' + focusArea + '\n'
}

scanPrompt += 'Run: cd /Users/boyadboz/REPOS/ccattler\n' +
  'Find largest files: find . -name "*.go" ! -name "*_test.go" ! -path "./vendor/*" -exec wc -l {} \\; | sort -rn | head -20\n' +
  'IMPORTANT: Only include dead code where you actually ran grep and got 0 hits. Do not guess.'

var scanResult = await agent(scanPrompt, { label: 'scanner', phase: 'Scan', schema: SCAN_SCHEMA })

if (!scanResult) {
  log('Scan returned no results')
  return { mechanical: { confirmed: [], falsePositives: [] }, judgmentNeeded: null, testsPass: false }
}

var mechanicalCount = scanResult.mechanical.deadCode.length + scanResult.mechanical.styleViolations.length
var judgmentCount = scanResult.judgmentNeeded.duplication.length + scanResult.judgmentNeeded.complexity.length

log('Found ' + mechanicalCount + ' mechanical + ' + judgmentCount + ' judgment-needed findings')

// Phase 2: Verify — agent grep-checks only the mechanical findings
phase('Verify')

if (mechanicalCount === 0) {
  log('No mechanical findings to verify')
  var verifyResult = { confirmed: [], falsePositives: [], summary: 'Nothing to verify' }
} else {
  var mechanicalList = ''
  scanResult.mechanical.deadCode.forEach(function(item) {
    mechanicalList += 'Dead code: ' + item.name + ' (' + item.kind + ') in ' + item.file + ' (scanner reported ' + item.grepHits + ' hits)\n'
  })
  scanResult.mechanical.styleViolations.forEach(function(item) {
    mechanicalList += 'Style: ' + item.rule + ' — ' + item.current + ' in ' + item.file + ':' + (item.line || '?') + '\n'
  })

  log('Verifying ' + mechanicalCount + ' mechanical findings via grep')

  var verifyResult = await agent(
    'Independently verify these findings by running grep commands. Do NOT trust the scanner — re-run every check.\n\n' +
    'Findings to verify:\n' + mechanicalList + '\n' +
    'For dead code: run grep -rn "\\bNAME\\b" --include="*.go" /Users/boyadboz/REPOS/ccattler/ and count references outside the definition.\n' +
    'For style violations: read the exact line and confirm the comment/name is missing.\n' +
    'Report each finding as verified (true) or false positive with reason.',
    { label: 'verifier', phase: 'Verify', schema: VERIFY_SCHEMA }
  )
}

var confirmedCount = verifyResult ? verifyResult.confirmed.filter(function(c) { return c.verified }).length : 0
var falsePositiveCount = verifyResult ? verifyResult.falsePositives.length : 0
log('Verified: ' + confirmedCount + ' confirmed, ' + falsePositiveCount + ' false positives')

// Phase 3: Baseline test
phase('Baseline')
log('Running test suite to confirm baseline is green')

var testResult = await agent(
  'Run the full CCattler test suite.\n' +
  'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go test -count=1 -race -timeout 120s ./...\n' +
  'Report pass/fail counts.',
  { label: 'test-runner', phase: 'Baseline', schema: TEST_SCHEMA }
)

if (testResult && testResult.allPassing) {
  log('Baseline green')
} else {
  log('Tests failing — fix before refactoring')
}

// Return everything — mechanical confirmed by agents, judgment for user
return {
  mechanical: {
    confirmed: verifyResult ? verifyResult.confirmed.filter(function(c) { return c.verified }) : [],
    falsePositives: verifyResult ? verifyResult.falsePositives : [],
  },
  judgmentNeeded: scanResult.judgmentNeeded,
  testsPass: testResult ? testResult.allPassing : false,
}

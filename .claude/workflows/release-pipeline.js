export const meta = {
  name: 'release-pipeline',
  description: 'Generate changelog, run full validation suite, check deployment, and prepare release notes',
  phases: [
    { title: 'Changelog', detail: 'project-manager generates changelog from git history' },
    { title: 'Validate', detail: 'parallel test suite + integration tests + security audit' },
    { title: 'Deployment', detail: 'devops-engineer checks Ansible roles and deployment configs' },
    { title: 'Release Notes', detail: 'technical-writer produces final release notes and doc updates' },
  ],
}

var releaseTag = (args && args.tag) || null
var previousTag = (args && args.previousTag) || null

var CHANGELOG_SCHEMA = {
  type: 'object',
  properties: {
    milestoneComplete: { type: 'boolean' },
    currentMilestone: { type: 'string' },
    allItemsChecked: { type: 'boolean' },
    uncheckedItems: {
      type: 'array',
      items: { type: 'string' },
    },
    added: {
      type: 'array',
      items: { type: 'string' },
    },
    changed: {
      type: 'array',
      items: { type: 'string' },
    },
    fixed: {
      type: 'array',
      items: { type: 'string' },
    },
    commitCount: { type: 'integer' },
    summary: { type: 'string' },
  },
  required: ['milestoneComplete', 'currentMilestone', 'added', 'changed', 'fixed', 'summary'],
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

var SECURITY_SCHEMA = {
  type: 'object',
  properties: {
    posture: { type: 'string', enum: ['strong', 'adequate', 'needs_work', 'critical_gaps'] },
    criticalFindings: { type: 'integer' },
    highFindings: { type: 'integer' },
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          severity: { type: 'string', enum: ['critical', 'high', 'medium', 'low'] },
          area: { type: 'string' },
          description: { type: 'string' },
        },
        required: ['severity', 'area', 'description'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['posture', 'criticalFindings', 'highFindings', 'summary'],
}

var DEPLOY_SCHEMA = {
  type: 'object',
  properties: {
    ansibleValid: { type: 'boolean' },
    releaseWorkflowValid: { type: 'boolean' },
    installScriptValid: { type: 'boolean' },
    issues: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          area: { type: 'string' },
          description: { type: 'string' },
          severity: { type: 'string', enum: ['blocker', 'warning', 'info'] },
        },
        required: ['area', 'description', 'severity'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['ansibleValid', 'releaseWorkflowValid', 'installScriptValid', 'summary'],
}

// Phase 1: Changelog
phase('Changelog')

var tagRange = ''
if (releaseTag && previousTag) {
  tagRange = previousTag + '..' + releaseTag
} else {
  tagRange = 'latest 30 commits'
}

log('Generating changelog from ' + tagRange)

var changelog = await agent(
  'Generate a changelog for the CCattler project. ' +
  'Read CLAUDE.md to check the current milestone and which phase items are checked [x] vs unchecked [ ]. ' +
  (previousTag && releaseTag
    ? 'Run: cd /Users/boyadboz/REPOS/ccattler && git log ' + previousTag + '..' + releaseTag + ' --oneline --no-merges'
    : 'Run: cd /Users/boyadboz/REPOS/ccattler && git log --oneline -30 --no-merges') +
  '\nCategorize changes into Added (new features), Changed (modifications), Fixed (bug fixes). ' +
  'Write for users, not developers — describe what changed, not which files changed. ' +
  'Also verify: is the current milestone complete? Are all phase items checked?',
  { label: 'changelog-generator', phase: 'Changelog', schema: CHANGELOG_SCHEMA }
)

if (changelog && !changelog.milestoneComplete) {
  log('WARNING: Milestone not fully complete — ' + (changelog.uncheckedItems || []).length + ' unchecked items remain')
} else {
  log('Milestone confirmed complete, proceeding to validation')
}

// Phase 2: Validate (parallel — tests + security)
phase('Validate')
log('Running test suite and security audit in parallel')

var validationResults = await parallel([
  function() {
    return agent(
      'Run the full CCattler test suite for release validation. ' +
      'Run: cd /Users/boyadboz/REPOS/ccattler && go vet ./... && go test -count=1 -race -timeout 120s ./... ' +
      'This is a release gate — report every failure with full detail.',
      { label: 'test-runner', phase: 'Validate', schema: TEST_SCHEMA }
    )
  },
  function() {
    return agent(
      'Run a security audit of the CCattler project for release validation. ' +
      'Check: mTLS enforcement (no InsecureSkipVerify), secret handling (no plaintext in store), ' +
      'authentication (all endpoints require auth), authorization (RBAC+ABAC on store ops), ' +
      'input validation (no injection paths), audit logging (append-only, complete). ' +
      'Run grep checks from .claude/agents/security-audit.md. ' +
      'This is a release gate — report anything that could be a security issue.',
      { label: 'security-audit', phase: 'Validate', schema: SECURITY_SCHEMA }
    )
  },
])

var testResult = validationResults[0]
var securityResult = validationResults[1]

var blockers = []

if (testResult && !testResult.allPassing) {
  blockers.push('Test failures: ' + testResult.failed)
}
if (securityResult && securityResult.criticalFindings > 0) {
  blockers.push('Critical security findings: ' + securityResult.criticalFindings)
}

if (blockers.length > 0) {
  log('RELEASE BLOCKED: ' + blockers.join('; '))
} else {
  log('Validation passed, checking deployment')
}

// Phase 3: Deployment check
phase('Deployment')
log('Checking Ansible roles, release workflow, and install scripts')

var deployCheck = await agent(
  'Check CCattler deployment infrastructure for release readiness. ' +
  'Verify: ' +
  '1. Ansible syntax: cd /Users/boyadboz/REPOS/ccattler && ansible-playbook --syntax-check deploy/ansible/site.yml 2>&1 || echo "ansible not installed locally, checking files manually" ' +
  '2. Read deploy/ansible/roles/*/tasks/main.yml — check for hardcoded paths, missing handlers ' +
  '3. Read .github/workflows/release.yml — verify it builds binaries and deploy tarball on tag push ' +
  '4. Read deploy/install.sh — verify it downloads from GitHub releases, not manual paths ' +
  '5. Check firewall rules include ports 9770 (API), 15353 (DNS), 2379-2380 (etcd) ' +
  '6. Check certificate paths are consistent across server and agent Ansible templates',
  { label: 'devops-engineer', phase: 'Deployment', schema: DEPLOY_SCHEMA }
)

if (deployCheck && deployCheck.issues.some(function(issue) { return issue.severity === 'blocker' })) {
  blockers.push('Deployment blockers: ' + deployCheck.issues.filter(function(issue) { return issue.severity === 'blocker' }).length)
  log('Deployment has blockers')
} else {
  log('Deployment checks passed')
}

// Phase 4: Release notes
phase('Release Notes')
log('Generating release notes and updating docs')

var changelogText = ''
if (changelog) {
  var sections = []
  if (changelog.added.length > 0) sections.push('Added: ' + changelog.added.join(', '))
  if (changelog.changed.length > 0) sections.push('Changed: ' + changelog.changed.join(', '))
  if (changelog.fixed.length > 0) sections.push('Fixed: ' + changelog.fixed.join(', '))
  changelogText = sections.join('. ')
}

await agent(
  'Update STATUS.md in the CCattler project to reflect release preparation. ' +
  'The changelog found: ' + changelogText + '. ' +
  'Blockers: ' + (blockers.length > 0 ? blockers.join('; ') : 'none') + '. ' +
  'If there are no blockers, update STATUS.md Recent Completions with today\'s date. ' +
  'If there are blockers, add them to Known Issues. ' +
  'Update metrics: run wc -l on Go files and count source/test files.',
  { label: 'status-updater', phase: 'Release Notes' }
)

return {
  releaseReady: blockers.length === 0,
  blockers: blockers,
  changelog: changelog ? {
    milestone: changelog.currentMilestone,
    milestoneComplete: changelog.milestoneComplete,
    addedCount: changelog.added.length,
    changedCount: changelog.changed.length,
    fixedCount: changelog.fixed.length,
  } : null,
  tests: testResult ? {
    passed: testResult.passed,
    failed: testResult.failed,
  } : null,
  security: securityResult ? {
    posture: securityResult.posture,
    criticalFindings: securityResult.criticalFindings,
  } : null,
  deployment: deployCheck ? {
    ansibleValid: deployCheck.ansibleValid,
    releaseWorkflowValid: deployCheck.releaseWorkflowValid,
    blockerCount: deployCheck.issues.filter(function(i) { return i.severity === 'blocker' }).length,
  } : null,
}

export const meta = {
  name: 'deployment-validation',
  description: 'Review infrastructure changes and validate deployment on Vagrant',
  phases: [
    { title: 'Review', detail: 'devops-engineer reviews infrastructure changes' },
    { title: 'Test', detail: 'deployment-tester validates cluster on Vagrant' },
  ],
}

var REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    ansibleSyntaxValid: { type: 'boolean' },
    issuesFound: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          description: { type: 'string' },
          severity: { type: 'string', enum: ['blocker', 'warning', 'info'] },
        },
        required: ['file', 'description', 'severity'],
      },
    },
    portsCorrect: { type: 'boolean' },
    certsConsistent: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['ansibleSyntaxValid', 'issuesFound', 'summary'],
}

var DEPLOY_SCHEMA = {
  type: 'object',
  properties: {
    deploySucceeded: { type: 'boolean' },
    smokeTests: {
      type: 'object',
      properties: {
        serverRunning: { type: 'boolean' },
        etcdHealthy: { type: 'boolean' },
        agentsConnected: { type: 'boolean' },
        serviceDeployment: { type: 'boolean' },
        networking: { type: 'boolean' },
        dns: { type: 'boolean' },
        noErrorLogs: { type: 'boolean' },
      },
      required: ['serverRunning', 'etcdHealthy', 'agentsConnected', 'serviceDeployment'],
    },
    nodeCount: { type: 'integer' },
    failureDetails: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          check: { type: 'string' },
          error: { type: 'string' },
        },
        required: ['check', 'error'],
      },
    },
    summary: { type: 'string' },
  },
  required: ['deploySucceeded', 'smokeTests', 'summary'],
}

var target = (args && args.target) || 'vagrant'
var provider = (args && args.provider) || 'libvirt'

// Phase 1: Review infrastructure changes
phase('Review')
log('Reviewing deployment infrastructure for ' + target)

var reviewResult = await agent(
  'Review CCattler deployment infrastructure for correctness.\n' +
  'Check the following:\n' +
  '1. Ansible syntax: cd /Users/boyadboz/REPOS/ccattler && ansible-playbook --syntax-check deploy/ansible/site.yml 2>&1 || echo "checking files manually"\n' +
  '2. Read all Ansible role task files: find deploy/ansible/roles -name "main.yml" -exec echo "---" \\; -exec cat {} \\;\n' +
  '3. Check for hardcoded IPs that should be in inventory\n' +
  '4. Check firewall rules include ports: 9770 (API), 15353 (DNS), 2379-2380 (etcd), 80 (proxy)\n' +
  '5. Check certificate paths are consistent between server template and agent template\n' +
  '6. Read .github/workflows/release.yml — verify build targets and artifact names\n' +
  '7. Read deploy/install.sh — verify it downloads from GitHub releases URL\n' +
  '8. Check Vagrantfile: cd /Users/boyadboz/REPOS/ccattler/deploy && cat Vagrantfile\n' +
  'Report any issues found with severity.',
  { label: 'devops-engineer', phase: 'Review', schema: REVIEW_SCHEMA }
)

var hasBlockers = reviewResult && reviewResult.issuesFound.some(function(issue) { return issue.severity === 'blocker' })

if (hasBlockers) {
  log('Blockers found in infrastructure review — deployment test may fail')
} else {
  log('Infrastructure review clean, proceeding to deployment test')
}

// Phase 2: Deployment test
phase('Test')
log('Running deployment test on ' + target + ' (' + provider + ')')

var deployPrompt =
  'Test CCattler deployment on ' + target + '.\n\n'

if (target === 'vagrant') {
  deployPrompt +=
    'Steps:\n' +
    '1. Pre-deployment checks:\n' +
    '   cd /Users/boyadboz/REPOS/ccattler\n' +
    '   ls -la deploy/install.sh deploy/Vagrantfile\n' +
    '   cat deploy/ansible/inventory.ini\n\n' +
    '2. Check if Vagrant VMs are already running:\n' +
    '   cd deploy && vagrant status 2>&1\n\n' +
    '3. If VMs exist, check their state. If not running or need fresh test:\n' +
    '   NOTE: Do NOT destroy or create VMs without user confirmation.\n' +
    '   Instead, SSH into existing VMs and run smoke tests.\n\n' +
    '4. Smoke tests (run on existing VMs if available):\n' +
    '   vagrant ssh ctrl -c "cca status" 2>&1\n' +
    '   vagrant ssh ctrl -c "cca get nodes" 2>&1\n' +
    '   vagrant ssh ctrl -c "cca get services" 2>&1\n' +
    '   vagrant ssh ctrl -c "cca get instances" 2>&1\n' +
    '   vagrant ssh ctrl -c "ps aux | grep cca" 2>&1\n\n' +
    '5. Test service deployment:\n' +
    '   vagrant ssh ctrl -c "ls /home/*/ccattler/examples/*.ccl 2>/dev/null && cca get services" 2>&1\n\n' +
    '6. Test networking:\n' +
    '   vagrant ssh ctrl -c "curl -s -o /dev/null -w \'%{http_code}\' -H \'Host: web\' http://localhost:80" 2>&1\n' +
    '   vagrant ssh ctrl -c "dig @127.0.0.1 -p 15353 web.ccattler.local +short" 2>&1\n\n' +
    '7. Check logs for errors:\n' +
    '   vagrant ssh ctrl -c "journalctl -u ccattler-server --no-pager -n 20 2>/dev/null || echo no systemd" 2>&1\n\n' +
    'If VMs are not running, report that and list what manual steps are needed.'
} else {
  deployPrompt +=
    'Target: ' + target + '\n' +
    'Check deploy/ansible/inventory.ini for target hosts.\n' +
    'Verify SSH connectivity and prerequisites.\n' +
    'Report what would need to happen for a deployment test.'
}

var deployResult = await agent(deployPrompt, { label: 'deployment-tester', phase: 'Test', schema: DEPLOY_SCHEMA })

var overallPass = deployResult && deployResult.deploySucceeded &&
  deployResult.smokeTests.serverRunning &&
  deployResult.smokeTests.etcdHealthy &&
  deployResult.smokeTests.agentsConnected

if (overallPass) {
  log('Deployment validation PASSED')
} else {
  var failedChecks = []
  if (deployResult && deployResult.failureDetails) {
    deployResult.failureDetails.forEach(function(failure) {
      failedChecks.push(failure.check)
    })
  }
  log('Deployment validation FAILED' + (failedChecks.length > 0 ? ': ' + failedChecks.join(', ') : ''))
}

return {
  target: target,
  provider: provider,
  reviewClean: reviewResult ? !hasBlockers : false,
  reviewIssues: reviewResult ? reviewResult.issuesFound.length : 0,
  deploySucceeded: deployResult ? deployResult.deploySucceeded : false,
  smokeTests: deployResult ? deployResult.smokeTests : null,
  overallPass: overallPass,
  failures: deployResult ? deployResult.failureDetails : [],
}

#!/bin/bash
# PostToolUse hook for Bash tool — auto-logs every command to COMMAND_HISTORY.md.
# Receives tool call data as JSON on stdin. Filters sensitive information.

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)"
if [ -z "$REPO_ROOT" ]; then
    exit 0
fi

HISTORY_PATH="$REPO_ROOT/COMMAND_HISTORY.md"

# Create the file with header if it doesn't exist.
if [ ! -f "$HISTORY_PATH" ]; then
    printf '# Command History\n\n| Timestamp | Command | Description |\n|-----------|---------|-------------|\n' > "$HISTORY_PATH"
fi

# Read JSON from stdin.
INPUT=$(cat)

COMMAND=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('tool_input',{}).get('command',''))" 2>/dev/null)
DESCRIPTION=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('tool_input',{}).get('description',''))" 2>/dev/null)

if [ -z "$COMMAND" ]; then
    exit 0
fi

# --- Sensitive information filtering ---
# Redact SSH private key paths.
COMMAND=$(echo "$COMMAND" | sed -E 's|-i [^ ]*\.ssh/[^ ]*|-i [SSH_KEY]|g')
# Redact password/token/secret flags.
COMMAND=$(echo "$COMMAND" | sed -E 's|(--password\|--token\|--secret)[= ][^ ]*|\1 [REDACTED]|g')
# Redact Bearer tokens.
COMMAND=$(echo "$COMMAND" | sed -E 's|Bearer [A-Za-z0-9_.+-]+|Bearer [REDACTED]|g')
# Redact environment variable assignments with sensitive names.
COMMAND=$(echo "$COMMAND" | sed -E 's|(PASSWORD\|TOKEN\|SECRET\|API_KEY\|PRIVATE_KEY)=[^ ]*|\1=[REDACTED]|g')
# Redact base64-looking long strings (>40 chars, likely keys/tokens).
COMMAND=$(echo "$COMMAND" | sed -E 's|[A-Za-z0-9+/]{40,}=*|[REDACTED_CREDENTIAL]|g')

# Truncate long commands for readability.
if [ ${#COMMAND} -gt 120 ]; then
    COMMAND="${COMMAND:0:117}..."
fi

# Escape pipe and backtick characters for markdown table.
COMMAND=$(echo "$COMMAND" | sed 's/|/\\|/g')
DESCRIPTION=$(echo "$DESCRIPTION" | sed 's/|/\\|/g')

TIMESTAMP=$(date '+%Y-%m-%d %H:%M')

echo "| $TIMESTAMP | \`$COMMAND\` | $DESCRIPTION |" >> "$HISTORY_PATH"

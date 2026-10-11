#!/usr/bin/env bash
# Release bookkeeping script.
#
# Usage:
#   scripts/release.sh <milestone> <tag>
#
# Example:
#   scripts/release.sh M89 v1.17.0-beta
#
# What it does:
#   1. Validates the milestone exists in milestones.md
#   2. Updates CLAUDE.md status line
#   3. Generates changelog entry from milestones.md
#   4. Moves the <Badge text="latest" /> marker to the new version
#   5. Commits all bookkeeping changes
#   6. Creates an annotated git tag
#   7. Prints the push command
#
# After running, review with "git log --oneline -2" and "git diff HEAD~1",
# then push: git push && git push origin <tag>

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLAUDE_MD="${REPO_ROOT}/CLAUDE.md"
MILESTONES_MD="${REPO_ROOT}/milestones.md"
CHANGELOG_MD="${REPO_ROOT}/website/changelog.md"

# --- Argument parsing ---

if [[ $# -lt 2 ]]; then
    echo "Usage: scripts/release.sh <milestone> <tag>"
    echo "Example: scripts/release.sh M89 v1.17.0-beta"
    exit 1
fi

MILESTONE="$1"
TAG="$2"

# Validate milestone format (M followed by digits).
if [[ ! "$MILESTONE" =~ ^M[0-9]+$ ]]; then
    echo "Error: milestone must be in format M<number> (e.g. M89), got: ${MILESTONE}"
    exit 1
fi

# Validate tag format (v followed by semver).
if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    echo "Error: tag must be in format v<major>.<minor>.<patch>[-suffix] (e.g. v1.17.0-beta), got: ${TAG}"
    exit 1
fi

MILESTONE_NUMBER="${MILESTONE#M}"

# --- Validate milestone exists in milestones.md ---

MILESTONE_LINE=$(grep "^| ${MILESTONE} " "$MILESTONES_MD" 2>/dev/null || true)
if [[ -z "$MILESTONE_LINE" ]]; then
    echo "Error: ${MILESTONE} not found in milestones.md"
    echo "Add the milestone row before running this script."
    exit 1
fi

# Extract milestone title: "M89 — Template Conventions & Auto-Loading Values"
MILESTONE_TITLE=$(echo "$MILESTONE_LINE" | sed 's/^| \(M[0-9]* — [^|]*\) |.*/\1/' | sed 's/ *$//')

# Extract milestone summary (third column).
MILESTONE_SUMMARY=$(echo "$MILESTONE_LINE" | awk -F'|' '{print $4}' | sed 's/^ *//;s/ *$//')

echo "==> Milestone: ${MILESTONE_TITLE}"
echo "==> Tag: ${TAG}"
echo "==> Summary: ${MILESTONE_SUMMARY}"
echo ""

# --- Check for uncommitted changes ---

if [[ -n "$(git status --porcelain)" ]]; then
    echo "Error: working tree is dirty. Commit or stash changes first."
    git status --short
    exit 1
fi

# --- Check tag doesn't already exist ---

if git rev-parse "$TAG" >/dev/null 2>&1; then
    echo "Error: tag ${TAG} already exists."
    exit 1
fi

# --- Update CLAUDE.md status line ---

echo "==> Updating CLAUDE.md..."

# Extract the short title after the dash (e.g. "Template Conventions & Auto-Loading Values").
SHORT_TITLE=$(echo "$MILESTONE_TITLE" | sed "s/^${MILESTONE} — //")

# Build new status line, escaping & and \ for sed replacement safety.
NEW_STATUS="**Completed through:** ${MILESTONE} — Phase ${MILESTONE_NUMBER} ${SHORT_TITLE}. M1–${MILESTONE} complete. Gates A–I resolved."
SAFE_STATUS=$(printf '%s' "$NEW_STATUS" | sed 's/[&\\/]/\\&/g')

# Replace the existing status line.
sed -i '' "s|^\*\*Completed through:\*\*.*|${SAFE_STATUS}|" "$CLAUDE_MD"

# --- Update changelog ---

echo "==> Updating changelog..."

TODAY=$(date +%Y-%m-%d)

# Build the anchor ID: v1.17.0-beta -> v1-17-0-beta
ANCHOR_ID=$(echo "$TAG" | tr '.' '-')

# Remove <Badge text="latest" /> from the current latest entry.
sed -i '' 's/ <Badge type="tip" text="latest" \/>//' "$CHANGELOG_MD"

# Build the new changelog header.
CHANGELOG_HEADER="## ${TAG} <Badge type=\"tip\" text=\"latest\" /> {#${ANCHOR_ID}}"

# Create the new entry as a temp file, then prepend it.
TEMP_ENTRY=$(mktemp)
cat > "$TEMP_ENTRY" << ENTRY_EOF
${CHANGELOG_HEADER}

**${TODAY}** — ${SHORT_TITLE}.

- **${SHORT_TITLE}** (${MILESTONE}) — ${MILESTONE_SUMMARY}

---

ENTRY_EOF

# Insert after the "All notable releases" line (line 3).
sed -i '' "3r ${TEMP_ENTRY}" "$CHANGELOG_MD"
rm -f "$TEMP_ENTRY"

# --- Commit ---

echo "==> Committing bookkeeping..."

git add "$CLAUDE_MD" "$CHANGELOG_MD"
git commit -m "${MILESTONE}: release bookkeeping for ${TAG}

Update CLAUDE.md status, generate changelog entry from milestones.md."

# --- Tag ---

echo "==> Creating tag ${TAG}..."

git tag -a "$TAG" -m "${MILESTONE_TITLE}"

# --- Done ---

echo ""
echo "============================================"
echo "  Release ${TAG} ready!"
echo ""
echo "  Milestone: ${MILESTONE_TITLE}"
echo "  Commit:    $(git rev-parse --short HEAD)"
echo "  Tag:       ${TAG}"
echo ""
echo "  Review:"
echo "    git log --oneline -2"
echo "    git diff HEAD~1"
echo ""
echo "  Push:"
echo "    git push && git push origin ${TAG}"
echo "============================================"

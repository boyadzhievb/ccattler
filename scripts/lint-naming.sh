#!/usr/bin/env bash
# lint-naming.sh — Enforces CCattler naming conventions from CLAUDE.md:
#   - No single-letter variable names (except _ and standard Go: ok, err)
#   - No single-letter receiver names
#   - No cryptic abbreviations (ctrl, inst, svc, rel, val, mu, etc.)
#
# Exits 0 if clean, 1 if violations found.
# Usage: ./scripts/lint-naming.sh [file ...]
#   With no arguments, scans all .go files under the project root.

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ $# -gt 0 ]; then
    FILES=("$@")
else
    mapfile -t FILES < <(find "$PROJECT_ROOT" -name '*.go' \
        -not -path '*/vendor/*' \
        -not -path '*/.git/*' \
        -not -path '*/website/*' \
        -not -name 'lint-naming.sh')
fi

VIOLATIONS=0

for filepath in "${FILES[@]}"; do
    relative_path="${filepath#"$PROJECT_ROOT"/}"

    # Check for single-letter receiver names: func (x *Type)
    while IFS= read -r line; do
        if [ -n "$line" ]; then
            echo "RECEIVER: $relative_path: $line"
            VIOLATIONS=$((VIOLATIONS + 1))
        fi
    done < <(grep -nE '^\s*func\s+\([a-zA-Z]\s+\*?[A-Z]' "$filepath" 2>/dev/null || true)

    # Check for single-letter short variable declarations: x := ...
    # Excludes: _ (blank identifier), ok (map/type assertion), standard loop/test patterns
    while IFS= read -r line; do
        if [ -n "$line" ]; then
            # Skip lines that are just `_ :=` or `ok :=` or `ok, err :=`
            if echo "$line" | grep -qE '^\s*[0-9]+:\s*(//|/\*|_\s*:=|ok\s*:=|ok,)'; then
                continue
            fi
            echo "SHORT-VAR: $relative_path: $line"
            VIOLATIONS=$((VIOLATIONS + 1))
        fi
    done < <(grep -nE '\b[a-zA-Z]\s*:=' "$filepath" 2>/dev/null \
        | grep -vE '(ok\s*:=|_\s*:=|ok,\s*err\s*:=|err\s*:=)' \
        | grep -vE '//.*[a-zA-Z]\s*:=' \
        || true)

    # Check for banned abbreviations in variable declarations
    while IFS= read -r line; do
        if [ -n "$line" ]; then
            echo "ABBREVIATION: $relative_path: $line"
            VIOLATIONS=$((VIOLATIONS + 1))
        fi
    done < <(grep -nE '\b(ctrl|inst|svc|rel|mu)\s*(:=|\s+\S)' "$filepath" 2>/dev/null \
        | grep -vE '(//|/\*|".*ctrl.*"|".*inst.*"|".*svc.*")' \
        || true)
done

if [ "$VIOLATIONS" -gt 0 ]; then
    echo ""
    echo "Found $VIOLATIONS naming violation(s). See CLAUDE.md Code Style Rules."
    exit 1
else
    echo "No naming violations found."
    exit 0
fi

#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../website"

echo "==> Installing docs dependencies..."
npm install --silent

echo "==> Building docs to docs/..."
npx vitepress build

echo "==> Done. Output in docs/"

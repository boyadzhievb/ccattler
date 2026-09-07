#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> Generating site-data.json from codebase..."
go run scripts/generate-site-data.go

echo "==> Installing website dependencies..."
cd website
npm install --silent

echo "==> Building website to docs/..."
VITE_CONFIG_NATIVE_IGNORE_WARNING=true npx vite build

echo "==> Done. Output in docs/"

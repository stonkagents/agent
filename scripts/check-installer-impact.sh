#!/usr/bin/env bash
# Feature: F-025 (Auto-Update)
# Story: US-025-10 (DMG v0.4.0 Release)
# Purpose: Detect when code changes affect binaries/assets bundled in macOS DMG or Windows MSI
#
# Usage:
#   ./scripts/check-installer-impact.sh              # check staged files
#   ./scripts/check-installer-impact.sh --base main   # compare branch vs base

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# Paths that affect installer output (DMG/MSI binaries and assets)
INSTALLER_PATHS=(
  "cmd/daemon/"
  "cmd/controller/"
  "cmd/genkeys/"
  "internal/daemon/"
  "internal/controller/"
  "internal/config/"
  "internal/update/"
  "pkg/"
  "installer/"
  "scripts/macos/"
  "scripts/build-msi.ps1"
  "scripts/build-release.ps1"
  "assets/"
  "go.mod"
  "go.sum"
)

# Parse args
BASE_BRANCH=""
if [[ "${1:-}" == "--base" ]] && [[ -n "${2:-}" ]]; then
  BASE_BRANCH="$2"
fi

# Get changed files
if [[ -n "$BASE_BRANCH" ]]; then
  CHANGED_FILES=$(git diff --name-only "$BASE_BRANCH"...HEAD 2>/dev/null || git diff --name-only "$BASE_BRANCH" HEAD 2>/dev/null || echo "")
else
  CHANGED_FILES=$(git diff --name-only --cached 2>/dev/null || echo "")
  # Also include unstaged changes
  UNSTAGED=$(git diff --name-only 2>/dev/null || echo "")
  if [[ -n "$UNSTAGED" ]]; then
    CHANGED_FILES=$(printf "%s\n%s" "$CHANGED_FILES" "$UNSTAGED" | sort -u)
  fi
fi

if [[ -z "$CHANGED_FILES" ]]; then
  exit 0
fi

# Check for matches
IMPACTED=()
while IFS= read -r file; do
  for path in "${INSTALLER_PATHS[@]}"; do
    if [[ "$file" == "$path"* ]] || [[ "$file" == "$path" ]]; then
      IMPACTED+=("$file")
      break
    fi
  done
done <<< "$CHANGED_FILES"

if [[ ${#IMPACTED[@]} -eq 0 ]]; then
  exit 0
fi

# Print warning
echo ""
echo "⚠️  INSTALLER IMPACT DETECTED"
echo "────────────────────────────"
echo "Changed files affecting installers:"
for file in "${IMPACTED[@]}"; do
  # Determine which installers are affected
  TARGETS=""
  case "$file" in
    cmd/daemon/*|internal/daemon/*|internal/config/*|pkg/*|go.mod|go.sum)
      TARGETS="DMG + MSI (daemon binary)" ;;
    cmd/controller/*|internal/controller/*|internal/update/*)
      TARGETS="DMG + MSI (controller binary)" ;;
    cmd/genkeys/*)
      TARGETS="DMG + MSI (genkeys binary)" ;;
    installer/*)
      TARGETS="DMG (Swift installer)" ;;
    scripts/macos/*)
      TARGETS="DMG (build scripts)" ;;
    scripts/build-msi.ps1|scripts/build-release.ps1)
      TARGETS="MSI (Windows build)" ;;
    assets/*)
      TARGETS="DMG (assets)" ;;
    *)
      TARGETS="installer" ;;
  esac
  printf "  %-40s → %s\n" "$file" "$TARGETS"
done
echo ""
echo "Action required:"
echo "  macOS:   ./scripts/macos/sign-release.sh <VERSION>"
echo "  Windows: .\\scripts\\build-msi.ps1 -Version \"<VERSION>\""
echo ""

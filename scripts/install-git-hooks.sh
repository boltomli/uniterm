#!/usr/bin/env sh
# Point this clone's git hooks at the versioned .githooks/ directory.
#
# Git does not version core.hooksPath, so every clone must opt in once. Run
# this after `git clone` (and after pulling a change that adds/updates a hook).
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

git config core.hooksPath .githooks
chmod +x .githooks/* 2>/dev/null || true

echo "Git hooks installed from .githooks/ (core.hooksPath=.githooks)."
echo "Commits touching frontend/ now run 'npm run typecheck' before they land."

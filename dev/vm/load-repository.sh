#!/usr/bin/env bash
# Runs as the ordinary session user, retaining P's remote and scoped Git key.
set -euo pipefail
source_commit=$1
bundle=/home/p/p-lab-repository.bundle
git bundle verify "$bundle"
head=$(git rev-parse --verify HEAD 2>/dev/null || true)
if test -z "$head"; then
  if test -n "$(git status --porcelain --untracked-files=all --ignored)"; then
    echo 'Refusing to seed a workspace containing user changes.' >&2
    exit 1
  fi
  git fetch "$bundle" HEAD
  git reset --hard "$source_commit"
elif test "$head" != "$source_commit"; then
  echo 'Refusing to replace an existing session commit during repository seeding.' >&2
  exit 1
fi
if test -n "$(git status --porcelain --untracked-files=all --ignored)"; then
  echo 'Refusing to complete seeding with user changes in the workspace.' >&2
  exit 1
fi
git push origin HEAD:refs/heads/main
rm -- "$bundle" /home/p/p-lab-load-repository.sh

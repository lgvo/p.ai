# shellcheck shell=bash
# Step 7b1: the commit bundle an image build receives. The bundled source
# driver's Export writes a captured commit from P's repository into a private
# staging directory; no builder or Nix expression runs here.
set -E
umask 077
step_dir="$P_TEST_TMP/step-19"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
on_error() {
  printf 'P_COMMIT_BUNDLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in export.err refused.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
}
trap 'on_error "$?" "$LINENO"' ERR

repo=$(timeout 25 bundle-fixture prepare "$state" app | jq -er '.repository_path')
test -d "$repo/objects"
work="$step_dir/work"
git clone -q "$repo" "$work" 2> "$step_dir/clone.err"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
mkdir -p "$work/scripts"
printf '{ outputs = _: {}; }\n' > "$work/flake.nix"
printf 'flake.nix export-ignore\n' > "$work/.gitattributes"
printf '#!/bin/sh\nexit 0\n' > "$work/scripts/build"
chmod 755 "$work/scripts/build"
ln -s scripts/build "$work/build-link"
git -C "$work" add .
git -C "$work" commit -qm first
first=$(git -C "$work" rev-parse HEAD)
git -C "$work" push -q origin HEAD:refs/heads/main

# The dirty checkout is never an input to the export.
printf 'dirty checkout\n' > "$work/flake.nix"
printf 'untracked\n' > "$work/untracked"
refs_before=$(git -C "$repo" for-each-ref)
objects_before=$(git -C "$repo" count-objects -v)

export_bundle() {
  local commit="$1" staging="$step_dir/staging-$2"
  mkdir -m 0700 "$staging"
  timeout 25 bundle-fixture export "$state" app "$commit" "$staging" \
    > "$step_dir/$2.json" 2> "$step_dir/export.err"
  jq -e --arg commit "$commit" --arg path "$staging/source.bundle" \
    '.commit == $commit and .ref == "refs/p/source" and .path == $path' "$step_dir/$2.json" >/dev/null
  # Only the finished bundle remains: no temporary repository, no partial file.
  test "$(find "$staging" -mindepth 1 -printf '%P\n')" = source.bundle
  test "$(git bundle list-heads "$staging/source.bundle")" = "$commit refs/p/source"
}
# A clone of the bundle checks out exactly the commit's tree and history.
assert_clone() {
  local bundle="$1" commit="$2" clone="$step_dir/clone-$3"
  git -c advice.detachedHead=false clone -q --revision=refs/p/source "$bundle" "$clone"
  test "$(git -C "$clone" rev-parse HEAD)" = "$commit"
  test "$(git -C "$clone" ls-tree -r HEAD)" = "$(git -C "$repo" ls-tree -r "$commit")"
  test "$(git -C "$clone" rev-list --all)" = "$(git -C "$repo" rev-list "$commit")"
  test -z "$(git -C "$clone" status --porcelain --ignored)"
}

export_bundle "$first" first
bundle="$step_dir/staging-first/source.bundle"
# P's repository is only read: no new ref and no new object.
test "$(git -C "$repo" for-each-ref)" = "$refs_before"
test "$(git -C "$repo" count-objects -v)" = "$objects_before"
assert_clone "$bundle" "$first" first
clone="$step_dir/clone-first"
test "$(cat "$clone/flake.nix")" = '{ outputs = _: {}; }'
test "$(cat "$clone/.gitattributes")" = 'flake.nix export-ignore'
test "$(readlink "$clone/build-link")" = scripts/build
test -x "$clone/scripts/build"
test ! -e "$clone/untracked"

# Moving the branch after the export leaves the bundle as it was.
bundle_digest=$(sha256sum < "$bundle")
git -C "$work" add flake.nix
git -C "$work" commit -qm second
second=$(git -C "$work" rev-parse HEAD)
git -C "$work" push -q origin HEAD:refs/heads/main
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$second"
test "$(sha256sum < "$bundle")" = "$bundle_digest"
test "$(git bundle list-heads "$bundle")" = "$first refs/p/source"
assert_clone "$bundle" "$first" after-move

# An ancestor of a head is still exportable; the new tip carries its history.
export_bundle "$first" ancestor
assert_clone "$step_dir/staging-ancestor/source.bundle" "$first" ancestor
export_bundle "$second" second
assert_clone "$step_dir/staging-second/source.bundle" "$second" second
test "$(cat "$step_dir/clone-second/flake.nix")" = 'dirty checkout'
test ! -e "$step_dir/clone-second/untracked"

refuse() {
  local label="$1" commit="$2" staging="$step_dir/staging-$3"
  mkdir -m 0700 "$staging"
  if timeout 25 bundle-fixture export "$state" app "$commit" "$staging" \
    > "$step_dir/refused.json" 2> "$step_dir/refused.err"; then
    echo "bundle unexpectedly exported: $label" >&2
    exit 1
  fi
  test ! -s "$step_dir/refused.json"
  test -s "$step_dir/refused.err"
  test -z "$(find "$staging" -mindepth 1 -print -quit)"
}
inside=$(printf inside | git -C "$repo" hash-object -w --stdin)
hidden_tree=$(printf '100644 blob %s\thidden\0' "$inside" | git -C "$repo" mktree -z)
hidden_commit=$(git -C "$repo" commit-tree "$hidden_tree" -m hidden)
git -C "$repo" update-ref refs/hidden/snapshot "$hidden_commit"
refuse 'hidden-only commit' "$hidden_commit" hidden
refuse 'missing commit' ffffffffffffffffffffffffffffffffffffffff missing
refuse 'tree' "$hidden_tree" tree
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$second"
test -z "$(git -C "$repo" for-each-ref refs/p/)"
echo P_COMMIT_BUNDLE_PASS

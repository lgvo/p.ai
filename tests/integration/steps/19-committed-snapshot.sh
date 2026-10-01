# shellcheck shell=bash
# Step 7b1: selected source-Git WASI observation and fixed committed-tree capture.
# This exercises host staging only; no Nix expression or builder is run.
set -E
umask 077
step_dir="$P_TEST_TMP/step-19"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
activation="$step_dir/activation.json"
snapshot_pid=
snapshot_path=
cleanup() {
  if test -n "$snapshot_pid"; then
    kill -TERM "$snapshot_pid" 2>/dev/null || true
    wait "$snapshot_pid" 2>/dev/null || true
  fi
}
on_error() {
  printf 'P_COMMITTED_SNAPSHOT_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in capture.err refused.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$digest" --arg id "$id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$activation"
p plugins activate "$activation" | jq -e '.[0].capability == "source-git"' >/dev/null
repo=$(timeout 25 snapshot-fixture prepare "$state" "$activation" app | jq -er '.repository_path')
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
first_tree=$(git -C "$work" rev-parse 'HEAD^{tree}')
git -C "$work" push -q origin HEAD:refs/heads/main

# The dirty checkout is never an input to source observation or materialization.
printf 'dirty checkout\n' > "$work/flake.nix"
printf 'untracked\n' > "$work/untracked"
start_capture() {
  local kind="$1" value="$2" output="$3" ready=false
  : > "$output"
  snapshot-fixture capture "$state" "$activation" app "$kind" "$value" hold \
    > "$output" 2> "$step_dir/capture.err" &
  snapshot_pid=$!
  for ((attempt=0; attempt<100; attempt++)); do
    if jq -e '.path and .commit_oid and .tree_oid' "$output" >/dev/null 2>&1; then ready=true; break; fi
    if ! kill -0 "$snapshot_pid" 2>/dev/null; then echo 'snapshot fixture exited before readiness' >&2; exit 1; fi
    sleep 0.1
  done
  test "$ready" = true
  snapshot_path=$(jq -er '.path' "$output")
  test -d "$snapshot_path"
}
release_capture() {
  local old_path="$snapshot_path"
  kill -TERM "$snapshot_pid"
  wait "$snapshot_pid"
  snapshot_pid=
  snapshot_path=
  test ! -e "$old_path"
  test -d "$repo/objects"
}
assert_first_tree() {
  local output="$1"
  jq -e --arg first "$first" --arg tree "$first_tree" \
    '.project == "app" and .commit_oid == $first and .tree_oid == $tree and .entries == 5 and .bytes > 0' \
    "$output" >/dev/null
  test "$(cat "$snapshot_path/flake.nix")" = '{ outputs = _: {}; }'
  test "$(cat "$snapshot_path/.gitattributes")" = 'flake.nix export-ignore'
  test "$(readlink "$snapshot_path/build-link")" = scripts/build
  test "$(cat "$snapshot_path/build-link")" = "$(cat "$snapshot_path/scripts/build")"
  test "$(stat -c %a "$snapshot_path")" = 500
  test "$(stat -c %a "$snapshot_path/flake.nix")" = 400
  test "$(stat -c %a "$snapshot_path/scripts/build")" = 500
  test ! -e "$snapshot_path/.git"
  test ! -e "$snapshot_path/untracked"
}
start_capture branch refs/heads/main "$step_dir/first.json"
assert_first_tree "$step_dir/first.json"

# Move the selected ref while the immutable snapshot is held open.
git -C "$work" add flake.nix
git -C "$work" commit -qm second
second=$(git -C "$work" rev-parse HEAD)
git -C "$work" push -q origin HEAD:refs/heads/main
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$second"
assert_first_tree "$step_dir/first.json"
release_capture
start_capture commit "$first" "$step_dir/ancestor.json"
assert_first_tree "$step_dir/ancestor.json"
release_capture
start_capture branch refs/heads/main "$step_dir/second.json"
jq -e --arg second "$second" '.commit_oid == $second' "$step_dir/second.json" >/dev/null
test "$(cat "$snapshot_path/flake.nix")" = 'dirty checkout'
test ! -e "$snapshot_path/untracked"
release_capture

blob() { printf '%s' "$1" | git -C "$repo" hash-object -w --stdin; }
tree() { git -C "$repo" mktree -z; }
commit_tree() { git -C "$repo" commit-tree "$1" -p "$second" -m fixture; }
inside=$(blob inside)
up=$(blob ../file)
good_dir=$(printf '120000 blob %s\tup\0' "$up" | tree)
good_tree=$({ printf '100644 blob %s\tfile\0' "$inside"; printf '040000 tree %s\tdir\0' "$good_dir"; } | tree)
good_commit=$(commit_tree "$good_tree")
git -C "$repo" update-ref refs/heads/safe-link "$good_commit"
start_capture branch refs/heads/safe-link "$step_dir/safe.json"
test "$(cat "$snapshot_path/dir/up")" = inside
test "$(readlink "$snapshot_path/dir/up")" = ../file
release_capture

refuse() {
  local label="$1" kind="$2" value="$3"
  if timeout 25 snapshot-fixture capture "$state" "$activation" app "$kind" "$value" \
    > "$step_dir/refused.json" 2> "$step_dir/refused.err"; then
    echo "snapshot unexpectedly accepted: $label" >&2
    exit 1
  fi
  test ! -s "$step_dir/refused.json"
  test -s "$step_dir/refused.err"
  test -z "$(find "$state/source-snapshots" -mindepth 1 -maxdepth 1 ! -name outside -print)"
}
# A lexical in-root target can escape after resolving an earlier symlink.
back=$(blob ..)
bad_dir=$(printf '120000 blob %s\tb\0' "$back" | tree)
escape=$(blob dir/b/../outside)
bad_tree=$({ printf '120000 blob %s\ta\0' "$escape"; printf '040000 tree %s\tdir\0' "$bad_dir"; } | tree)
bad_commit=$(commit_tree "$bad_tree")
git -C "$repo" update-ref refs/heads/escape "$bad_commit"
outside="$state/source-snapshots/outside"
printf 'host sentinel\n' > "$outside"
refuse 'composed symlink escape' branch refs/heads/escape
test "$(cat "$outside")" = 'host sentinel'

gitlink_tree=$(printf '160000 commit %s\tdependency\0' "$first" | tree)
gitlink_commit=$(commit_tree "$gitlink_tree")
git -C "$repo" update-ref refs/heads/gitlink "$gitlink_commit"
refuse 'gitlink' branch refs/heads/gitlink
large_link=$(head -c 4097 /dev/zero | tr '\0' a | git -C "$repo" hash-object -w --stdin)
large_tree=$({ printf '100644 blob %s\ta-ok\0' "$inside"; printf '120000 blob %s\tz-large\0' "$large_link"; } | tree)
large_commit=$(commit_tree "$large_tree")
git -C "$repo" update-ref refs/heads/large-link "$large_commit"
refuse 'bounded symlink' branch refs/heads/large-link
hidden_tree=$(printf '100644 blob %s\thidden\0' "$inside" | tree)
hidden_commit=$(git -C "$repo" commit-tree "$hidden_tree" -m hidden)
git -C "$repo" update-ref refs/hidden/snapshot "$hidden_commit"
refuse 'hidden-only commit' commit "$hidden_commit"
refuse 'missing commit' commit ffffffffffffffffffffffffffffffffffffffff
# A selected package without source observation must not fall back to native Git.
alternate="$step_dir/alternate.json"
alternate_digest=$(p plugins conformance "$P_TEST_GIT_ALTERNATE" | jq -er '.sha256')
alternate_id=$(jq -er '.id' "$P_TEST_GIT_ALTERNATE/plugin.json")
jq -n --arg path "$P_TEST_GIT_ALTERNATE" --arg digest "$alternate_digest" --arg id "$alternate_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$alternate"
if timeout 25 snapshot-fixture capture "$state" "$alternate" app branch refs/heads/main \
  > "$step_dir/refused.json" 2> "$step_dir/refused.err"; then
  echo 'alternate source-Git package unexpectedly captured a source' >&2
  exit 1
fi
test ! -s "$step_dir/refused.json"
test -s "$step_dir/refused.err"
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$second"
test -z "$(find "$state/source-snapshots" -mindepth 1 -maxdepth 1 ! -name outside -print)"
test "$(cat "$outside")" = 'host sentinel'
echo P_COMMITTED_SNAPSHOT_PASS

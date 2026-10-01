# shellcheck shell=bash
# Runs inside the disposable product-test VM. The private fixture invokes
# source-Git backend methods against seeded repositories; these are test-only
# calls and do not implement public project or session lifecycle operations.
umask 077
set -E
step_dir="$P_TEST_TMP/step-08"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
activation="$step_dir/activation.json"
fixture_pid=

stop_fixture() {
  if test -n "$fixture_pid"; then
    kill -TERM "$fixture_pid" 2>/dev/null || true
    for ((attempt=0; attempt<50; attempt++)); do
      if ! kill -0 "$fixture_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$fixture_pid" 2>/dev/null; then kill -KILL "$fixture_pid" 2>/dev/null || true; fi
    wait "$fixture_pid" 2>/dev/null || true
    fixture_pid=
  fi
}
on_error() {
  printf 'P_GIT_COMMITTED_SOURCE_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in fixture.err refused.json refused.err; do
    if test -f "$step_dir/$log"; then
      printf '%s (last 2 KiB):\n' "$log" >&2
      tail -c 2048 "$step_dir/$log" >&2 || true
    fi
  done
}
trap stop_fixture EXIT
trap 'on_error "$?" "$LINENO"' ERR

package_digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
package_id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$package_digest" --arg id "$package_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$activation"
p plugins activate "$activation" | jq -e '.[0].capability == "source-git"' >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/server" >/dev/null
start_fixture() {
  : > "$step_dir/fixture-ready.json"
  git-fixture serve "$state" "$1" 127.0.0.1:0 "$step_dir/server" \
    > "$step_dir/fixture-ready.json" 2> "$step_dir/fixture.err" &
  fixture_pid=$!
  ready=false
  for ((attempt=0; attempt<100; attempt++)); do
    if jq -e '.schema == "p.git-fixture/v1" and .control' "$step_dir/fixture-ready.json" >/dev/null 2>&1; then
      ready=true
      break
    fi
    if ! kill -0 "$fixture_pid" 2>/dev/null; then
      echo 'Git source fixture exited before readiness' >&2
      exit 1
    fi
    sleep 0.1
  done
  test "$ready" = true
  socket=$(jq -er '.control' "$step_dir/fixture-ready.json")
  test -S "$socket"
}
start_fixture "$activation"
fixture() { timeout 15 git-fixture call "$socket" "$1"; }
observe() {
  fixture "$(jq -nc --arg project "$1" --arg kind "$2" --arg value "$3" \
    '{schema:"p.git-fixture/v1",kind:"observe-source",project:$project,selector:{kind:$kind,value:$value}}')" |
    jq -er '.result.commit_oid'
}
create_branch() {
  fixture "$(jq -nc --arg project "$1" --arg branch "$2" --arg oid "$3" \
    '{schema:"p.git-fixture/v1",kind:"create-branch",project:$project,branch:$branch,commit_oid:$oid}')" |
    jq -e --arg branch "$2" --arg oid "$3" '.ok and .result.branch == $branch and .result.commit_oid == $oid' >/dev/null
}
expect_denied() {
  label="$1"
  payload="$2"
  if fixture "$payload" > "$step_dir/refused.json" 2> "$step_dir/refused.err"; then
    echo "source operation unexpectedly accepted: $label" >&2
    exit 1
  fi
  jq -e '.ok == false and (.error | type == "string" and length > 0)' "$step_dir/refused.json" >/dev/null
}
expect_observe_denied() {
  observe_app_refs_before=$(all_refs "$repo")
  observe_other_refs_before=$(all_refs "$other_repo")
  expect_denied "$4" "$(jq -nc --arg project "$1" --arg kind "$2" --arg value "$3" \
    '{schema:"p.git-fixture/v1",kind:"observe-source",project:$project,selector:{kind:$kind,value:$value}}')"
  test "$(all_refs "$repo")" = "$observe_app_refs_before"
  test "$(all_refs "$other_repo")" = "$observe_other_refs_before"
}
all_refs() { git -C "$1" for-each-ref --format='%(refname) %(objectname) %(symref)'; }
heads() { git -C "$1" for-each-ref --format='%(refname) %(objectname) %(symref)' refs/heads/; }
expect_create_denied() {
  target_repo="$1"
  project="$2"
  branch="$3"
  oid="$4"
  label="$5"
  before=$(heads "$target_repo")
  expect_denied "$label" "$(jq -nc --arg project "$project" --arg branch "$branch" --arg oid "$oid" \
    '{schema:"p.git-fixture/v1",kind:"create-branch",project:$project,branch:$branch,commit_oid:$oid}')"
  test "$(heads "$target_repo")" = "$before"
}
commit_tree() {
  if test -n "$3"; then
    git -C "$1" commit-tree "$2" -p "$3" -m "$4"
  else
    git -C "$1" commit-tree "$2" -m "$4"
  fi
}

fixture '{"schema":"p.git-fixture/v1","kind":"ping"}' | jq -e '.result.status == "ready"' >/dev/null
repo=$(fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"app"}' | jq -er '.result.repository_path')
other_repo=$(fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"other"}' | jq -er '.result.repository_path')
test "$(git -C "$repo" symbolic-ref HEAD)" = refs/heads/main
expect_observe_denied app branch refs/heads/main 'unborn main'
expect_observe_denied app commit 0000000000000000000000000000000000000000 'zero object ID'

export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
tree=$(git -C "$repo" hash-object -t tree -w --stdin < /dev/null)
first=$(commit_tree "$repo" "$tree" '' first)
second=$(commit_tree "$repo" "$tree" "$first" second)
git -C "$repo" update-ref refs/heads/main "$second"
test "$(observe app branch refs/heads/main)" = "$second"
test "$(observe app commit "$first")" = "$first"
create_branch app feature "$first"
test "$(git -C "$repo" rev-parse refs/heads/feature)" = "$first"
expect_create_denied "$repo" app feature "$first" 'same-tip existing destination'
expect_create_denied "$repo" app feature "$second" 'different-tip existing destination'
test "$(git -C "$repo" rev-parse refs/heads/feature)" = "$first"

third=$(commit_tree "$repo" "$tree" "$second" third)
git -C "$repo" update-ref refs/heads/main "$third" "$second"
test "$(observe app branch refs/heads/main)" = "$third"
test "$(git -C "$repo" rev-parse refs/heads/feature)" = "$first"
create_branch app captured-earlier "$second"
test "$(git -C "$repo" rev-parse refs/heads/captured-earlier)" = "$second"

blob=$(printf 'blob\n' | git -C "$repo" hash-object -w --stdin)
hidden=$(commit_tree "$repo" "$tree" '' hidden-only)
prefix=$(commit_tree "$repo" "$tree" '' prefix-only)
tag_only=$(commit_tree "$repo" "$tree" '' tag-only)
git -C "$repo" update-ref refs/p/secret "$hidden"
git -C "$repo" update-ref refs/heads-private/sibling "$prefix"
git -C "$repo" update-ref refs/tags/v1 "$tag_only"
other_tree=$(git -C "$other_repo" hash-object -t tree -w --stdin < /dev/null)
other_commit=$(commit_tree "$other_repo" "$other_tree" '' other-project)
git -C "$other_repo" update-ref refs/heads/main "$other_commit"
other_before=$(heads "$other_repo")

expect_observe_denied app branch refs/heads/missing 'missing branch'
expect_observe_denied app branch refs/p/secret 'hidden ref branch selector'
expect_observe_denied app branch refs/heads-private/sibling 'heads-prefix sibling selector'
expect_observe_denied app branch refs/tags/v1 'tag branch selector'
expect_observe_denied app branch refs/heads/../bad 'malformed branch selector'
expect_observe_denied app commit HEAD 'symbolic commit selector'
expect_observe_denied app commit ffffffffffffffffffffffffffffffffffffffff 'missing commit object'
expect_observe_denied app commit "$blob" 'blob source'
expect_observe_denied app commit "$tree" 'tree source'
expect_observe_denied app commit "$hidden" 'hidden-only commit'
expect_observe_denied app commit "$prefix" 'heads-prefix sibling-only commit'
expect_observe_denied app commit "$tag_only" 'tag-only commit'
expect_observe_denied app commit "$other_commit" 'cross-project commit'

expect_create_denied "$repo" app ../invalid "$second" 'malformed destination'
expect_create_denied "$repo" app missing-object ffffffffffffffffffffffffffffffffffffffff 'missing destination commit'
expect_create_denied "$repo" app blob-source "$blob" 'blob destination source'
expect_create_denied "$repo" app tree-source "$tree" 'tree destination source'
expect_create_denied "$repo" app hidden-source "$hidden" 'hidden destination source'
expect_create_denied "$repo" app prefix-source "$prefix" 'prefix sibling destination source'
expect_create_denied "$repo" app tag-source "$tag_only" 'tag-only destination source'
expect_create_denied "$repo" app cross-project "$other_commit" 'cross-project destination source'
test "$(heads "$other_repo")" = "$other_before"

# A symbolic destination counts as occupied even when its target is missing.
git -C "$repo" symbolic-ref refs/heads/resolved-link refs/heads/main
git -C "$repo" symbolic-ref refs/heads/dangling-link refs/heads/does-not-exist
expect_create_denied "$repo" app resolved-link "$second" 'resolved symbolic destination'
expect_create_denied "$repo" app dangling-link "$second" 'dangling symbolic destination'
test "$(git -C "$repo" symbolic-ref refs/heads/resolved-link)" = refs/heads/main
test "$(git -C "$repo" symbolic-ref refs/heads/dangling-link)" = refs/heads/does-not-exist
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$third"
if git -C "$repo" show-ref --verify --quiet refs/heads/does-not-exist; then
  echo 'dangling symbolic destination created its target' >&2
  exit 1
fi

git -C "$repo" update-ref refs/heads/namespace-parent "$first"
expect_create_denied "$repo" app namespace-parent/child "$second" 'destination below existing ref'
git -C "$repo" update-ref refs/heads/namespace-child/leaf "$first"
expect_create_denied "$repo" app namespace-child "$second" 'destination above existing ref'
test "$(git -C "$repo" rev-parse refs/heads/namespace-parent)" = "$first"
test "$(git -C "$repo" rev-parse refs/heads/namespace-child/leaf)" = "$first"
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$third"
test "$(heads "$other_repo")" = "$other_before"

# The independently authored alternate package does not implement the new
# commands. Replacing the package must refuse them with no native fallback.
alternate="$step_dir/alternate.json"
alternate_digest=$(p plugins conformance "$P_TEST_GIT_ALTERNATE" | jq -er '.sha256')
alternate_id=$(jq -er '.id' "$P_TEST_GIT_ALTERNATE/plugin.json")
jq -n --arg path "$P_TEST_GIT_ALTERNATE" --arg digest "$alternate_digest" --arg id "$alternate_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$alternate"
stop_fixture
test ! -e "$socket"
start_fixture "$alternate"
fixture '{"schema":"p.git-fixture/v1","kind":"ping"}' | jq -e '.result.status == "ready"' >/dev/null
expect_observe_denied app branch refs/heads/main 'alternate package source observation'
expect_create_denied "$repo" app alternate-cannot-create "$second" 'alternate package branch creation'
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$third"
test "$(git -C "$repo" symbolic-ref refs/heads/resolved-link)" = refs/heads/main
test "$(git -C "$repo" symbolic-ref refs/heads/dangling-link)" = refs/heads/does-not-exist

stop_fixture
test ! -e "$socket"
echo P_GIT_COMMITTED_SOURCE_PASS

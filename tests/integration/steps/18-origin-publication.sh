# shellcheck shell=bash
# Step 6c1 substrate: real host OpenSSH and selected WASI source-Git packages.
# Public assignment, confirmation, and RPC publication belong to step 6c2.
set -E
umask 077
step_dir="$P_TEST_TMP/step-18"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
fixture_ssh="$step_dir/ssh"
mkdir -m 0700 "$fixture_ssh"
account_ssh=/home/pdev/.ssh
made_account_ssh=false
if test ! -d "$account_ssh"; then mkdir -m 0700 "$account_ssh"; made_account_ssh=true; fi
if test -e "$account_ssh/config" || test -L "$account_ssh/config"; then
  echo 'publication fixture requires an unused pdev SSH config' >&2
  exit 1
fi
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
server_pid=
cleanup() {
  rm -f -- "$account_ssh/config"
  if test "$made_account_ssh" = true; then rmdir "$account_ssh" 2>/dev/null || true; fi
  if test -n "$server_pid"; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
}
on_error() {
  printf 'P_ORIGIN_PUBLICATION_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in server.err call.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$target"
  p plugins activate "$target" | jq -e '.[0].capability == "source-git"' >/dev/null
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/bundled.json"
activate "$P_TEST_GIT_ALTERNATE" "$step_dir/alternate.json"

work="$step_dir/work"
remote="$step_dir/full.git"
empty="$step_dir/empty.git"
git init -q -b main "$work"
git init --bare -q "$remote"
git init --bare -q "$empty"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
printf 'base\n' > "$work/file"
git -C "$work" add file
git -C "$work" commit -qm base
base=$(git -C "$work" rev-parse HEAD)
printf 'source\n' > "$work/file"
git -C "$work" commit -qam source
source=$(git -C "$work" rev-parse HEAD)
printf 'child\n' > "$work/file"
git -C "$work" commit -qam child
child=$(git -C "$work" rev-parse HEAD)
git -C "$work" checkout -q -b diverge "$base"
printf 'divergent\n' > "$work/file"
git -C "$work" commit -qam divergent
divergent=$(git -C "$work" rev-parse HEAD)
git -C "$work" tag -a -m preserved v1 "$base"
git -C "$work" push -q "$remote" \
  "$base:refs/heads/base" "$source:refs/heads/equal" \
  "$child:refs/heads/contains" "$divergent:refs/heads/divergent" \
  "$base:refs/heads/behind" "$base:refs/heads/raced" \
  refs/tags/v1:refs/tags/v1

ssh-keygen -q -t ed25519 -N '' -f "$step_dir/host" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$fixture_ssh/id_origin" >/dev/null
lost_marker="$step_dir/drop-accepted-response"
trace="$step_dir/server.trace"
: > "$trace"
origin-fixture serve-publish "$step_dir/host" "$fixture_ssh/id_origin.pub" \
  "$empty" "$remote" "$empty" "$trace" "$lost_marker" \
  > "$step_dir/server.json" 2> "$step_dir/server.err" &
server_pid=$!
ready=false
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.address | type=="string"' "$step_dir/server.json" >/dev/null 2>&1; then ready=true; break; fi
  if ! kill -0 "$server_pid" 2>/dev/null; then echo 'publication SSH fixture exited' >&2; exit 1; fi
  sleep 0.1
done
test "$ready" = true
port=$(jq -er '.address | split(":")[-1] | tonumber' "$step_dir/server.json")
awk '{print "fixture-origin " $1 " " $2}' "$step_dir/host.pub" > "$fixture_ssh/known_hosts"
cat > "$account_ssh/config" <<CONFIG
Host origin-fixture
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_ssh/known_hosts
  StrictHostKeyChecking yes
  IdentityFile $fixture_ssh/id_origin
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 2
CONFIG
url=pdev@origin-fixture:full.git
project=publication
source_ref=refs/heads/publish
p_repo=$(origin-fixture prepare-publish "$state" "$step_dir/bundled.json" "$project")
git -C "$work" push -q "$p_repo" "$source:$source_ref"
test "$(git -C "$p_repo" rev-parse "$source_ref")" = "$source"
test "$(git -C "$p_repo" symbolic-ref HEAD)" = refs/heads/main
# Repository-controlled settings must not redirect either origin transport.
git -C "$p_repo" config core.sshCommand false
git -C "$p_repo" config url.file:///nonexistent/.insteadOf 'pdev@origin-'
git -C "$p_repo" config push.followTags true
git -C "$p_repo" for-each-ref --format='%(refname) %(objectname)' > "$step_dir/p-refs.before"
test "$(cat "$step_dir/p-refs.before")" = "$source_ref $source"
cp "$p_repo/config" "$step_dir/p-config.before"
cp "$p_repo/HEAD" "$step_dir/p-head.before"
test ! -e "$p_repo/FETCH_HEAD"

refs_except() {
  git -C "$remote" for-each-ref --format='%(refname) %(objectname)' | awk -v ref="$1" '$1 != ref'
}
receive_count() { grep -c '^receive-pack ' "$trace" || true; }
call() { timeout 45 origin-fixture publish "$@" 2> "$step_dir/call.err"; }
case_publication() {
  local activation="$1" destination="$2" relation="$3" status="$4" expected_oid="$5"
  shift 5
  local other_before observed_before receives_before receives_after expected_receives response
  other_before=$(refs_except "$destination")
  observed_before=$(git -C "$remote" rev-parse -q --verify "$destination" 2>/dev/null || true)
  receives_before=$(receive_count)
  response=$(call run "$state" "$activation" "$project" "$url" "$source_ref" "$source" \
    "$destination" "$relation" "$status" "$@")
  jq -e --arg url "$url" --arg project "$project" --arg source_ref "$source_ref" \
    --arg source "$source" --arg destination "$destination" --arg observed "$observed_before" \
    --arg relation "$relation" --arg status "$status" \
    '.preview.project == $project and .preview.url == $url and
     .preview.source_ref == $source_ref and .preview.source_oid == $source and
     .preview.destination_ref == $destination and (.preview.destination_oid // "") == $observed and
     .preview.relation == $relation and
     .result.status == $status and .result.preview == .preview' <<< "$response" >/dev/null
  test "$(refs_except "$destination")" = "$other_before"
  test "$(git -C "$remote" rev-parse "$destination")" = "$expected_oid"
  receives_after=$(receive_count)
  expected_receives="$receives_before"
  if test "$relation" = absent || test "$relation" = fast_forward; then
    expected_receives=$((receives_before + 1))
  fi
  test "$receives_after" -eq "$expected_receives"
  test "$(git -C "$p_repo" for-each-ref --format='%(refname) %(objectname)')" = "$(cat "$step_dir/p-refs.before")"
  cmp -s "$p_repo/config" "$step_dir/p-config.before"
  cmp -s "$p_repo/HEAD" "$step_dir/p-head.before"
  test ! -e "$p_repo/FETCH_HEAD"
  test -z "$(git -C "$p_repo" remote)"
}

# Invalid, foreign, and expired previews cannot reach receive-pack.
receives_before=$(receive_count)
call no-observe "$state" "$step_dir/bundled.json" "$project" "$url" "$source_ref" "$source" refs/heads/behind fast_forward none >/dev/null
call wrong "$state" "$step_dir/bundled.json" "$project" "$url" "$source_ref" "$source" refs/heads/behind fast_forward none >/dev/null
call expired "$state" "$step_dir/bundled.json" "$project" "$url" "$source_ref" "$source" refs/heads/behind fast_forward none >/dev/null
test "$(receive_count)" -eq "$receives_before"

case_publication "$step_dir/bundled.json" refs/heads/new absent created "$source"
case_publication "$step_dir/bundled.json" refs/heads/equal equal satisfied "$source"
case_publication "$step_dir/bundled.json" refs/heads/contains destination_contains satisfied "$child"
case_publication "$step_dir/bundled.json" refs/heads/behind fast_forward advanced "$source"
case_publication "$step_dir/bundled.json" refs/heads/divergent divergent refused "$divergent"
case_publication "$step_dir/bundled.json" refs/heads/raced fast_forward refused "$divergent" "$remote" "$divergent"

# The alternate selected WASI package follows the same closed publication ABI.
case_publication "$step_dir/alternate.json" refs/heads/alternate absent created "$source"

# The server accepts the update and then reports SSH failure. No retry or
# refresh follows within this request; a later explicit request observes equal.
: > "$lost_marker"
uploads_before=$(grep -c '^upload-pack ' "$trace" || true)
receives_before=$(receive_count)
case_publication "$step_dir/bundled.json" refs/heads/uncertain absent outcome_unknown "$source"
test "$(grep -c '^upload-pack ' "$trace" || true)" -eq "$((uploads_before + 1))"
test "$(receive_count)" -eq "$((receives_before + 1))"
rm -f -- "$lost_marker"
uploads_before=$(grep -c '^upload-pack ' "$trace" || true)
case_publication "$step_dir/bundled.json" refs/heads/uncertain equal satisfied "$source"
test "$(receive_count)" -eq "$((receives_before + 1))"
test "$(grep -c '^upload-pack ' "$trace" || true)" -eq "$((uploads_before + 1))"

if grep -R -l -E 'BEGIN OPENSSH PRIVATE KEY|id_origin|origin-fixture' "$state" "$p_repo" >/dev/null; then
  echo 'publication credential or SSH alias leaked into P state' >&2
  exit 1
fi
echo P_ORIGIN_PUBLICATION_PASS

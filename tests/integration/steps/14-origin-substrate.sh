# shellcheck shell=bash
# Real host OpenSSH client against a bounded local upload-pack SSH fixture.
set -E
umask 077
step_dir="$P_TEST_TMP/step-14"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
fixture_home="$step_dir/home"
mkdir -m 0700 "$fixture_home" "$fixture_home/.ssh"
account_ssh=/home/pdev/.ssh
made_account_ssh=false
if test ! -d "$account_ssh"; then mkdir -m 0700 "$account_ssh"; made_account_ssh=true; fi
if test -e "$account_ssh/config" || test -L "$account_ssh/config"; then
  echo 'origin fixture requires an unused pdev SSH config in the disposable VM' >&2
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
  printf 'P_ORIGIN_SUBSTRATE_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
activate "$P_TEST_GIT_PLUGIN" "$step_dir/source.json"
activate "$P_TEST_GIT_ALTERNATE" "$step_dir/alternate.json"

empty="$step_dir/empty.git"
full="$step_dir/full.git"
flood="$step_dir/flood.git"
git init --bare -q "$empty"
git init --bare -q "$full"
git init --bare -q "$flood"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
tree=$(git -C "$full" hash-object -t tree -w --stdin < /dev/null)
first=$(git -C "$full" commit-tree "$tree" -m first)
second=$(git -C "$full" commit-tree "$tree" -p "$first" -m second)
git -C "$full" update-ref refs/heads/main "$first"
git -C "$full" tag -a -m release v1 "$first"
tag_oid=$(git -C "$full" rev-parse refs/tags/v1)
test "$tag_oid" != "$first"
# A valid advertisement larger than the origin runner's 300 KiB output cap.
# All names are valid refs, and the count stays below the 1024-ref limit.
test "$(git -C "$flood" hash-object -t tree -w --stdin < /dev/null)" = "$tree"
test "$(git -C "$flood" hash-object -t commit -w --stdin < <(git -C "$full" cat-file commit "$first"))" = "$first"
for ((n=0; n<900; n++)); do
  printf 'update refs/heads/flood-%0200d/%0100d %s\n' "$n" 0 "$first"
done | git -C "$flood" update-ref --stdin
test "$(git ls-remote --heads "$flood" | wc -c)" -gt $((300 << 10))

ssh-keygen -q -t ed25519 -N '' -f "$step_dir/host" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$fixture_home/.ssh/id_origin" >/dev/null
origin-fixture serve "$step_dir/host" "$fixture_home/.ssh/id_origin.pub" "$empty" "$full" "$flood" "$step_dir/server.trace" \
  > "$step_dir/server.json" 2> "$step_dir/server.err" &
server_pid=$!
ready=false
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.address | type=="string"' "$step_dir/server.json" >/dev/null 2>&1; then ready=true; break; fi
  if ! kill -0 "$server_pid" 2>/dev/null; then echo 'origin SSH fixture exited' >&2; exit 1; fi
  sleep 0.1
done
test "$ready" = true
port=$(jq -er '.address | split(":")[-1] | tonumber' "$step_dir/server.json")
test "$port" -ge 1024
awk '{print "fixture-origin " $1 " " $2}' "$step_dir/host.pub" > "$fixture_home/.ssh/known_hosts"
write_config() {
  local key="$1"
  cat > "$fixture_home/.ssh/config" <<CONFIG
Host origin-fixture
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_home/.ssh/known_hosts
  StrictHostKeyChecking yes
  IdentityFile $key
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 2
Host origin-unreachable
  HostName 127.0.0.1
  Port 1
  User pdev
  StrictHostKeyChecking yes
  IdentityFile $key
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 1
CONFIG
  cp "$fixture_home/.ssh/config" "$account_ssh/config"
}
write_config "$fixture_home/.ssh/id_origin"
scp_url='pdev@origin-fixture:full.git'
ssh_url='ssh://pdev@origin-fixture/empty.git'
call() { timeout 15 origin-fixture "$1" "$state" "$2" "$3" "$4" "${@:5}"; }
call observe-empty "$step_dir/source.json" empty "$ssh_url" | jq -e '.status == "observe-empty" and (.refs | length) == 0' >/dev/null
call observe "$step_dir/source.json" app "$scp_url" > "$step_dir/observed.json"
jq -e --arg first "$first" --arg tag "$tag_oid" \
  '.refs | length==2 and .[0].ref=="refs/heads/main" and .[0].commit_oid==$first and .[1].ref=="refs/tags/v1" and .[1].oid==$tag and .[1].commit_oid==$first' \
  "$step_dir/observed.json" >/dev/null
call fetch "$step_dir/source.json" app "$scp_url" refs/heads/main "$first" | jq -e --arg first "$first" '.commit==$first' >/dev/null
call fetch "$step_dir/source.json" app "$scp_url" refs/tags/v1 "$first" | jq -e --arg first "$first" '.commit==$first' >/dev/null
call race "$step_dir/source.json" app "$scp_url" refs/heads/main "$first" "$full" "$second" | jq -e '.status=="race"' >/dev/null
call fetch "$step_dir/source.json" app "$scp_url" refs/heads/main "$second" | jq -e --arg second "$second" '.commit==$second' >/dev/null
call alternate "$step_dir/alternate.json" alternate "$scp_url" refs/heads/main "$second" | jq -e --arg second "$second" '.commit==$second and .status=="alternate"' >/dev/null

# Every failed contact remains outside project association and repository creation.
write_config "$step_dir/missing-key"
trace_lines=$(wc -l < "$step_dir/server.trace")
call refuse "$step_dir/source.json" missing-key "$scp_url" >/dev/null
test "$(wc -l < "$step_dir/server.trace")" -eq "$trace_lines"
write_config "$fixture_home/.ssh/id_origin"
mv "$fixture_home/.ssh/known_hosts" "$fixture_home/.ssh/known_hosts.saved"
: > "$fixture_home/.ssh/known_hosts"
call refuse "$step_dir/source.json" unknown-host "$scp_url" >/dev/null
test "$(wc -l < "$step_dir/server.trace")" -eq "$trace_lines"
mv "$fixture_home/.ssh/known_hosts.saved" "$fixture_home/.ssh/known_hosts"
call refuse "$step_dir/source.json" unreachable 'pdev@origin-unreachable:full.git' >/dev/null
call refuse "$step_dir/source.json" invalid 'file:///tmp/forbidden.git' >/dev/null
call timeout "$step_dir/source.json" timed-out 'pdev@origin-fixture:hang.git' >/dev/null
grep -Fx '/hang.git' "$step_dir/server.trace" >/dev/null || grep -Fx 'hang.git' "$step_dir/server.trace" >/dev/null
call cancel "$step_dir/source.json" cancelled "$scp_url" >/dev/null
call refuse "$step_dir/source.json" excess-output 'pdev@origin-fixture:flood.git' >/dev/null
grep -Fx '/flood.git' "$step_dir/server.trace" >/dev/null || grep -Fx 'flood.git' "$step_dir/server.trace" >/dev/null

# The scratch bare repository carries cached objects only: no origin URL,
# credential, ordinary ref, remote-tracking ref, or protected ref.
repo=
for candidate in "$state"/repositories/*.git; do
  if git -C "$candidate" cat-file -e "$tag_oid" 2>/dev/null; then repo="$candidate"; break; fi
done
test -n "$repo"
test "$(git -C "$repo" cat-file -t "$first")" = commit
test "$(git -C "$repo" cat-file -t "$tag_oid")" = tag
test -z "$(git -C "$repo" for-each-ref --format='%(refname)')"
test -z "$(git -C "$repo" remote)"
if grep -R -l -E 'origin-fixture|id_origin|BEGIN OPENSSH PRIVATE KEY' "$repo" "$state" >/dev/null; then
  echo 'origin authority leaked into P repository or state' >&2
  exit 1
fi
echo P_ORIGIN_SUBSTRATE_PASS

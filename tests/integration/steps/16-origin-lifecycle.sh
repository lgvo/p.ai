# shellcheck shell=bash
# Public origin association and refresh through the configured production daemon.
# Active blank projects are seeded before daemon startup; origin-backed creation
# and source selection belong to the next lifecycle step.
set -E
umask 077
step_dir="$P_TEST_TMP/step-16"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
activation="$step_dir/git.json"
config="$step_dir/host.json"
account_ssh=/home/pdev/.ssh
fixture_ssh="$step_dir/ssh"
mkdir -m 0700 "$fixture_ssh"
made_account_ssh=false
if test ! -d "$account_ssh"; then mkdir -m 0700 "$account_ssh"; made_account_ssh=true; fi
if test -e "$account_ssh/config" || test -L "$account_ssh/config"; then
  echo 'origin lifecycle fixture requires an unused pdev SSH config' >&2
  exit 1
fi
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
seed_pid=
origin_pid=
stop_process() {
  local pid="$1"
  if test -z "$pid"; then return; fi
  kill -TERM "$pid" 2>/dev/null || true
  for ((attempt=0; attempt<50; attempt++)); do
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
  wait "$pid" 2>/dev/null || true
}
cleanup() {
  stop_process "$daemon_pid"
  stop_process "$seed_pid"
  stop_process "$origin_pid"
  rm -f -- "$account_ssh/config"
  if test "$made_account_ssh" = true; then rmdir "$account_ssh" 2>/dev/null || true; fi
}
on_error() {
  printf 'P_ORIGIN_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in daemon.err seed.err origin.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

rpc() { timeout 30 p api "$socket" "$@"; }
expect_error() {
  local kind="$1" method="$2" params="$3" response code
  case "$kind" in
    busy) code=-32003 ;;
    unavailable) code=-32004 ;;
    invalid_params) code=-32602 ;;
    *) echo "unknown expected RPC error kind: $kind" >&2; return 1 ;;
  esac
  if response=$(rpc "$method" "$params" 2> "$step_dir/api.err"); then
    echo "$method unexpectedly accepted $params" >&2
    return 1
  fi
  jq -e --arg kind "$kind" --argjson code "$code" \
    '.jsonrpc == "2.0" and .id == 1 and (has("result") | not) and
     .error.kind == $kind and .error.code == $code and
     (.error.message | type == "string" and length > 0)' <<< "$response" >/dev/null
}
change() {
  local key="$1" project="$2" kind="$3" expected="$4" url="${5:-}"
  rpc origin.change "$(jq -nc --arg key "$key" --arg project "$project" --arg kind "$kind" \
    --arg expected "$expected" --arg url "$url" \
    '{v:1,key:$key,project:$project,kind:$kind,expected_url:$expected} +
     (if $kind == "set" then {url:$url} else {} end)')"
}
inspect() { rpc origin.inspect "$(jq -nc --arg project "$1" '{v:1,project:$project}')"; }
refresh() { rpc origin.refresh "$(jq -nc --arg project "$1" '{v:1,project:$project}')"; }
sources() {
  rpc origin.sources "$(jq -nc --arg project "$1" --arg after "$2" --argjson limit "$3" \
    '{v:1,project:$project,after:$after,limit:$limit}')"
}
start_daemon() {
  p daemon "$config" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  for ((attempt=0; attempt<200; attempt++)); do
    if test -S "$socket" && timeout 2 p api "$socket" system.health |
      jq -e '.result.control_state == "ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'origin daemon exited' >&2; return 1; fi
    sleep 0.05
  done
  echo 'origin daemon did not become ready' >&2
  return 1
}

digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
plugin_id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$digest" --arg id "$plugin_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$activation"
p plugins activate "$activation" | jq -e '.[0].capability == "source-git"' >/dev/null

# Create registered blank projects with the existing test-only seed fixture.
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/seed-host" >/dev/null
git-fixture serve "$state" "$activation" 127.0.0.1:0 "$step_dir/seed-host" \
  > "$step_dir/seed-ready.json" 2> "$step_dir/seed.err" &
seed_pid=$!
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.control | type == "string"' "$step_dir/seed-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$seed_pid" 2>/dev/null; then echo 'seed fixture exited' >&2; exit 1; fi
  sleep 0.1
done
seed_socket=$(jq -er '.control' "$step_dir/seed-ready.json")
for project in app other; do
  printf '%s\n' "$(jq -nc --arg project "$project" '{schema:"p.git-fixture/v1",kind:"project",project:$project}')" |
    timeout 10 socat - "UNIX-CONNECT:$seed_socket" | jq -e '.ok == true' >/dev/null
done
stop_process "$seed_pid"
seed_pid=

empty="$step_dir/empty.git"
full="$step_dir/full.git"
git init --bare -q "$empty"
git init --bare -q "$full"
tree=$(git -C "$full" hash-object -t tree -w --stdin < /dev/null)
first=$(env GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid \
  GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$full" commit-tree "$tree" -m first)
git -C "$full" update-ref refs/heads/main "$first"
git -C "$full" update-ref refs/heads/zzz "$first"
env GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$full" tag -a -m release v1 "$first"
tag_oid=$(git -C "$full" rev-parse refs/tags/v1)
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/origin-host" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$fixture_ssh/id_origin" >/dev/null
origin-fixture serve "$step_dir/origin-host" "$fixture_ssh/id_origin.pub" \
  "$empty" "$full" "$empty" "$step_dir/origin.trace" \
  > "$step_dir/origin-ready.json" 2> "$step_dir/origin.err" &
origin_pid=$!
: > "$step_dir/origin.trace"
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.address | type == "string"' "$step_dir/origin-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$origin_pid" 2>/dev/null; then echo 'origin SSH fixture exited' >&2; exit 1; fi
  sleep 0.1
done
port=$(jq -er '.address | split(":")[-1] | tonumber' "$step_dir/origin-ready.json")
awk '{print "fixture-origin " $1 " " $2}' "$step_dir/origin-host.pub" > "$fixture_ssh/known_hosts"
write_ssh_config() {
  local key="$1"
  cat > "$account_ssh/config" <<CONFIG
Host origin-fixture
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_ssh/known_hosts
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
}
write_ssh_config "$fixture_ssh/id_origin"
full_url=pdev@origin-fixture:full.git
empty_url=ssh://pdev@origin-fixture/empty.git
bad_url=pdev@origin-unreachable:full.git
jq -n --arg state "$state" --arg activation "$activation" --arg id "$plugin_id" \
  '{schema:"p.host/v1",state_dir:$state,git:{activation_path:$activation,source_plugin_id:$id,listen:"127.0.0.1:0"}}' > "$config"
start_daemon
rpc system.capabilities | jq -e '.result.available | index("origin.change") != null and index("origin.sources") != null' >/dev/null
inspect app | jq -e '.result.origin | .status == "local-only" and .ref_count == 0 and (has("url") | not)' >/dev/null
trace_before=$(wc -l < "$step_dir/origin.trace")
refresh app | jq -e '.result.origin.status == "local-only"' >/dev/null
test "$(wc -l < "$step_dir/origin.trace")" = "$trace_before"
expect_error unavailable origin.change "$(jq -nc --arg url "$bad_url" '{v:1,key:"initial-failure",project:"app",kind:"set",expected_url:"",url:$url}')"
inspect app | jq -e '.result.origin.status == "local-only"' >/dev/null
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"

set_result=$(change initial-full app set '' "$full_url")
jq -e --arg url "$full_url" '.result.origin | .url == $url and .status == "fresh" and .ref_count == 3 and (.observed_at | length > 0)' <<< "$set_result" >/dev/null
inspect other | jq -e '.result.origin.status == "local-only"' >/dev/null
first_page=$(sources app '' 1)
jq -e --arg url "$full_url" --arg oid "$first" \
  '.result | .origin_url == $url and .status == "fresh" and .observation_status == "fresh" and
   .refs == [{ref:"refs/heads/main",oid:$oid,commit_oid:$oid}] and .next == "refs/heads/main"' <<< "$first_page" >/dev/null
second_page=$(sources app refs/heads/main 1)
jq -e --arg oid "$first" \
  '.result | .refs == [{ref:"refs/heads/zzz",oid:$oid,commit_oid:$oid}] and .next == "refs/heads/zzz"' <<< "$second_page" >/dev/null
sources app refs/heads/zzz 1 | jq -e --arg oid "$first" --arg tag "$tag_oid" \
  '.result | .refs == [{ref:"refs/tags/v1",oid:$tag,commit_oid:$oid}] and .next == ""' >/dev/null
full_sources=$(sources app '' 16 | jq -c '.result.refs')
expect_error busy origin.change "$(jq -nc --arg url "$empty_url" '{v:1,key:"stale-cas",project:"app",kind:"set",expected_url:"",url:$url}')"
expect_error unavailable origin.change "$(jq -nc --arg old "$full_url" --arg url "$bad_url" \
  '{v:1,key:"failed-replacement",project:"app",kind:"set",expected_url:$old,url:$url}')"
test "$(inspect app | jq -c '.result.origin')" = "$(jq -c '.result.origin' <<< "$set_result")"
test "$(sources app '' 16 | jq -c '.result.refs')" = "$full_sources"

# Losing SSH authority makes the completed view explicitly stale.
write_ssh_config "$step_dir/missing-key"
refresh app | jq -e --arg url "$full_url" \
  '.result.origin | .url == $url and .status == "unknown" and .ref_count == 3 and (.diagnostic | length > 0)' >/dev/null
sources app '' 16 | jq -e '.result | .status == "unknown" and .observation_status == "stale" and (.refs | length) == 3' >/dev/null
test "$(sources app '' 16 | jq -c '.result.refs')" = "$full_sources"
write_ssh_config "$fixture_ssh/id_origin"
refresh app | jq -e '.result.origin.status == "fresh" and .result.origin.ref_count == 3' >/dev/null
replacement=$(change replace-empty app set "$full_url" "$empty_url")
jq -e --arg url "$empty_url" '.result.origin | .url == $url and .status == "fresh" and .ref_count == 0' <<< "$replacement" >/dev/null
sources app '' 1 | jq -e '.result | .refs == [] and .next == "" and .observation_status == "fresh"' >/dev/null

# The completed key replays its recorded response even when contact is lost.
write_ssh_config "$step_dir/missing-key"
trace_before=$(wc -l < "$step_dir/origin.trace")
test "$(change replace-empty app set "$full_url" "$empty_url" | jq -c '.result.origin')" = "$(jq -c '.result.origin' <<< "$replacement")"
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"
expect_error busy origin.change "$(jq -nc --arg old "$full_url" --arg url "$bad_url" \
  '{v:1,key:"replace-empty",project:"app",kind:"set",expected_url:$old,url:$url}')"
stop_process "$daemon_pid"
daemon_pid=
start_daemon
inspect app | jq -e --arg url "$empty_url" '.result.origin | .url == $url and .status == "fresh" and .ref_count == 0' >/dev/null
sources app '' 1 | jq -e '.result | .refs == [] and .next == "" and .observation_status == "fresh"' >/dev/null
test "$(change replace-empty app set "$full_url" "$empty_url" | jq -c '.result.origin')" = "$(jq -c '.result.origin' <<< "$replacement")"

# Removal and refresh of a local-only project never contact SSH.
removed=$(change remove-empty app remove "$empty_url")
jq -e '.result.origin | .status == "local-only" and .ref_count == 0 and (has("url") | not)' <<< "$removed" >/dev/null
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"
refresh app | jq -e '.result.origin.status == "local-only"' >/dev/null
sources app '' 1 | jq -e '.result | .origin_url == "" and .observation_status == "none" and .refs == []' >/dev/null
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"

# Strict decoding includes alias spellings, duplicate keys, and extra fields.
expect_error invalid_params origin.change "$(jq -nc --arg url "$full_url" \
  '{v:1,key:"case-alias",project:"app",kind:"set",expected_url:"",URL:$url}')"
expect_error invalid_params origin.change '{"v":1,"v":1,"key":"bad","project":"app","kind":"remove","expected_url":""}'
expect_error invalid_params origin.refresh '{"v":1,"Project":"app"}'
expect_error invalid_params origin.inspect '{"v":1,"project":"app","extra":true}'
expect_error invalid_params origin.sources '{"v":1,"project":"app","limit":17}'
expect_error invalid_params origin.sources '{"v":1,"project":"app","limit":1,"after":"refs/heads/../bad"}'
echo P_ORIGIN_LIFECYCLE_PASS

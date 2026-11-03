# shellcheck shell=bash
# Test-only sessions through the production Incus runtime driver and its
# confined socket: create installs the selected guest files, start waits for
# the session host. Public project/session lifecycle is a later step.
set -E
umask 077
step_dir="$P_TEST_TMP/step-09"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
endpoint_prefix=/var/lib/p-vm/endpoints/pdev
names=()
pids=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
cleanup() {
  for name in "${names[@]}"; do inc delete --force "$name" >/dev/null 2>&1 || true; done
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  for name in "${names[@]}"; do rm -rf -- "${endpoint_prefix:?}/${name:?}"; done
}
on_error() {
  printf 'P_RUNTIME_ASSEMBLY_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for name in "${names[@]}"; do
    inc info "$name" --show-log 2>&1 | tail -c 2048 >&2 || true
    inc exec "$name" -- journalctl -u p-interactive.service -n 15 --no-pager 2>&1 | tail -c 2048 >&2 || true
  done
  tail -c 2048 "$step_dir/git.err" >&2 || true
  tail -c 1024 "$step_dir/start.err" >&2 || true
  tail -c 1024 "$step_dir/refused.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
expect_failure() {
  if "$@" > "$step_dir/unexpected.out" 2> "$step_dir/refused.err"; then
    printf 'Unexpected assembly acceptance: %s\n' "$*" >&2
    return 1
  fi
}
activate_package() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
# Hold this selected service in systemd's startup state until the scenario
# observes Running without host readiness. The gate is bounded and test-only;
# the changed bytes are pinned by the normal selected-package plan.
mkdir -m 0700 "$step_dir/host-package"
cp "$P_TEST_SOURCE/plugins/bundled/tmux-host/"* "$step_dir/host-package/"
sed '/^\[Install\]$/i ExecStartPost=/run/current-system/sw/bin/timeout 90 /run/current-system/sw/bin/bash -c "until test -e /etc/p/test-release; do sleep 0.1; done"' \
  "$step_dir/host-package/p-interactive.service" > "$step_dir/service-gated"
mv "$step_dir/service-gated" "$step_dir/host-package/p-interactive.service"
activate_package "$step_dir/host-package" "$step_dir/host.json" session.asset.install
activate_package "$P_TEST_GIT_PLUGIN" "$step_dir/source.json" git.project
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/server"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/blank"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/work"
mkdir -m 0700 "$step_dir/git-state"
git-fixture serve "$step_dir/git-state" "$step_dir/source.json" 127.0.0.1:0 "$step_dir/server" \
  > "$step_dir/git-ready.json" 2> "$step_dir/git.err" &
pids+=("$!")
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.control and .listen' "$step_dir/git-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "${pids[0]}" 2>/dev/null; then echo 'Git fixture exited before readiness' >&2; exit 1; fi
  sleep 0.1
done
git_control=$(jq -er '.control' "$step_dir/git-ready.json")
git_address=$(jq -er '.listen' "$step_dir/git-ready.json")
fixture() { timeout 15 git-fixture call "$git_control" "$1"; }
fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"assembly"}' | jq -e '.ok' >/dev/null
blank_result=$(fixture "$(jq -nc --arg key "$step_dir/blank.pub" \
  '{schema:"p.git-fixture/v1",kind:"seed-session",key:"assembly-main",project:"assembly",branch:"main",choice:"blank",public_key:$key}')")
blank_session=$(jq -er '.result.session_uuid' <<< "$blank_result")

prepare_endpoint() {
  local name="$1" endpoint attempt
  endpoint="$endpoint_prefix/$name"
  names+=("$name")
  mkdir -m 0755 "$endpoint"
  socat "UNIX-LISTEN:$endpoint/session.sock,fork,mode=666" EXEC:cat & pids+=("$!")
  socat "UNIX-LISTEN:$endpoint/git.sock,fork,mode=666" "TCP:$git_address" & pids+=("$!")
  for ((attempt=0; attempt<100; attempt++)); do
    if test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"; then return; fi
    sleep 0.1
  done
  echo 'endpoint listeners failed to start' >&2
  return 1
}
session_file() {
  local name="$1" session="$2" branch="$3" commit="$4" key="$5" target="$6"
  jq -n --arg instance 11111111-1111-4111-8111-111111111111 \
    --arg session "$session" --arg image "$fingerprint" --arg endpoints "$endpoint_prefix/$name" \
    --arg branch "$branch" --arg commit "$commit" --arg key "$step_dir/$key" \
    --arg server "$step_dir/server.pub" --arg host "$step_dir/host.json" \
    --arg source "$step_dir/source.json" \
    '{instance_uuid:$instance,session_uuid:$session,project:"assembly",branch:$branch,commit:$commit,
      image:$image,endpoints:$endpoints,host_activation:$host,source_activation:$source,
      git_key_file:$key,git_host_key_file:$server}' > "$target"
}
runtime() { timeout 190 runtime-fixture "$@"; }
exec_p() {
  local name="$1"
  shift
  inc exec "$name" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
wait_running_unready() {
  local spec="$1" deadline=$((SECONDS+60)) state
  while ((SECONDS < deadline)); do
    # During early boot the host cannot be read yet; that is no observation.
    if state=$(runtime inspect "$spec" 2> "$step_dir/observe.err"); then
      if jq -e '.state=="running" and (.host.ready|not) and (.host.unknown|not)' <<< "$state" >/dev/null; then
        return
      fi
    fi
    sleep 0.2
  done
  echo 'Running before host readiness was not observed' >&2
  return 1
}
blank_name="p-$blank_session"
prepare_endpoint "$blank_name"
session_file "$blank_name" "$blank_session" main '' blank "$step_dir/blank-session.json"
blank_spec="$step_dir/blank-session.json"
created=$(runtime create "$blank_spec")
jq -e '.state=="stopped" and .complete and .gated' <<< "$created" >/dev/null
blank_at=$(jq -er '.incarnation' <<< "$created")
# Completing the created runtime keeps every identical guest file.
runtime create "$blank_spec" "$blank_at" | jq -e --arg at "$blank_at" '.state=="stopped" and .complete and .incarnation==$at' >/dev/null
runtime create "$blank_spec" "$blank_at" | jq -e --arg at "$blank_at" '.state=="stopped" and .complete and .incarnation==$at' >/dev/null
test "$(inc file pull "$blank_name/etc/p/git/identity" - 2>/dev/null | sha256sum | cut -d' ' -f1)" = \
  "$(sha256sum "$step_dir/blank" | cut -d' ' -f1)"

# A refused completion must retain an existing altered file and its metadata.
inc file pull "$blank_name/etc/p/session.json" "$step_dir/session-original.json"
printf '%s\n' '{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/false"]}' > "$step_dir/session-changed.json"
inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/session-changed.json" "$blank_name/etc/p/session.json"
expect_failure runtime create "$blank_spec" "$blank_at"
test "$(inc file pull "$blank_name/etc/p/session.json" -)" = "$(cat "$step_dir/session-changed.json")"
inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/session-original.json" "$blank_name/etc/p/session.json"
inc file delete "$blank_name/etc/p/session.json"
inc file push --uid 0 --gid 0 --mode 0600 "$step_dir/session-original.json" "$blank_name/etc/p/session.json"
inc file pull "$blank_name/etc/p/session.json" "$step_dir/session-mode-check.json"
test "$(stat -c %a "$step_dir/session-mode-check.json")" = 600
expect_failure runtime create "$blank_spec" "$blank_at"
inc file delete "$blank_name/etc/p/session.json"
inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/session-original.json" "$blank_name/etc/p/session.json"
# A spec naming another workspace is refused; the installed one stays.
jq '.branch="other"' "$blank_spec" > "$step_dir/wrong-workspace.json"
inc file pull "$blank_name/etc/p/workspace.json" "$step_dir/workspace-before-refusal.json"
expect_failure runtime create "$step_dir/wrong-workspace.json" "$blank_at"
inc file pull "$blank_name/etc/p/workspace.json" "$step_dir/workspace-after-refusal.json"
cmp "$step_dir/workspace-before-refusal.json" "$step_dir/workspace-after-refusal.json"

# The selected unit gate makes Incus Running observable before host
# readiness, and Start returns only once the released host is ready.
runtime start "$blank_spec" > "$step_dir/blank-start.json" 2> "$step_dir/start.err" &
start_pid=$!
pids+=("$start_pid")
wait_running_unready "$blank_spec"
test ! -s "$step_dir/blank-start.json"
printf 'release\n' > "$step_dir/test-release"
inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/test-release" "$blank_name/etc/p/test-release"
wait "$start_pid"
jq -e --arg at "$blank_at" '.state=="running" and .host.ready and .incarnation==$at' "$step_dir/blank-start.json" >/dev/null
expect_failure runtime create "$blank_spec" "$blank_at"
test "$(exec_p "$blank_name" git symbolic-ref -q HEAD)" = refs/heads/main
expect_failure exec_p "$blank_name" git rev-parse --verify HEAD
fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"assembly","limit":8}' |
  jq -e '.ok==true and (.result.refs | length)==0' >/dev/null
exec_p "$blank_name" git config user.name P
exec_p "$blank_name" git config user.email p@example.invalid
exec_p "$blank_name" sh -c 'printf "first\n" > README'
exec_p "$blank_name" git add README
exec_p "$blank_name" git commit -qm first
exec_p "$blank_name" git push origin HEAD:main
main_oid=$(exec_p "$blank_name" git rev-parse HEAD)
fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"assembly","limit":8}' |
  jq -e --arg oid "$main_oid" '.ok==true and (.result.refs | any(.ref=="refs/heads/main" and .oid==$oid))' >/dev/null

# Established state is the private instance root, including altered local Git
# state. The captured OID belongs only to the second workspace's first boot.
exec_p "$blank_name" sh -c 'printf "dirty\n" > dirty; printf "private\n" > /home/p/private; printf "{}\n" > flake.nix'
exec_p "$blank_name" sh -c 'printf "local\n" > local.txt'
exec_p "$blank_name" git add local.txt flake.nix
exec_p "$blank_name" git commit -qm local
local_oid=$(exec_p "$blank_name" git rev-parse HEAD)
exec_p "$blank_name" git checkout -q --detach
store_path=$(exec_p "$blank_name" nix-store --add /workspace/README)
runtime stop "$blank_spec" | jq -e '.state=="stopped"' >/dev/null
runtime inspect "$blank_spec" | jq -e '.state=="stopped" and (.host.ready|not)' >/dev/null
runtime start "$blank_spec" | jq -e '.state=="running" and .host.ready' >/dev/null
test "$(exec_p "$blank_name" git rev-parse HEAD)" = "$local_oid"
test "$(exec_p "$blank_name" git symbolic-ref -q HEAD || true)" = ''
test "$(exec_p "$blank_name" cat dirty)" = dirty
test "$(exec_p "$blank_name" cat /home/p/private)" = private
exec_p "$blank_name" test -f flake.nix
exec_p "$blank_name" test -e "$store_path"
runtime stop "$blank_spec" | jq -e '.state=="stopped"' >/dev/null

# Capture a committed source ref and initialize a second, independent guest.
fixture "$(jq -nc --arg oid "$main_oid" \
  '{schema:"p.git-fixture/v1",kind:"create-branch",project:"assembly",branch:"work",commit_oid:$oid}')" | jq -e '.ok' >/dev/null
work_result=$(fixture "$(jq -nc --arg key "$step_dir/work.pub" \
  '{schema:"p.git-fixture/v1",kind:"seed-session",key:"assembly-work",project:"assembly",branch:"work",choice:"existing",public_key:$key}')")
work_session=$(jq -er '.result.session_uuid' <<< "$work_result")
work_name="p-$work_session"
prepare_endpoint "$work_name"
session_file "$work_name" "$work_session" work "$main_oid" work "$step_dir/work-session.json"
work_spec="$step_dir/work-session.json"
created=$(runtime create "$work_spec")
jq -e '.state=="stopped" and .complete and .gated' <<< "$created" >/dev/null
work_at=$(jq -er '.incarnation' <<< "$created")
# A spec naming another source commit is refused; the installed workspace stays.
jq '.commit=("0"*40)' "$work_spec" > "$step_dir/wrong-source.json"
inc file pull "$work_name/etc/p/workspace.json" "$step_dir/work-workspace-before-refusal.json"
expect_failure runtime create "$step_dir/wrong-source.json" "$work_at"
inc file pull "$work_name/etc/p/workspace.json" "$step_dir/work-workspace-after-refusal.json"
cmp "$step_dir/work-workspace-before-refusal.json" "$step_dir/work-workspace-after-refusal.json"
# Released in advance, the selected unit gate lets the first start finish.
inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/test-release" "$work_name/etc/p/test-release"
runtime start "$work_spec" | jq -e --arg at "$work_at" '.state=="running" and .host.ready and .incarnation==$at' >/dev/null
test "$(exec_p "$work_name" git rev-parse HEAD)" = "$main_oid"
test "$(exec_p "$work_name" git symbolic-ref -q HEAD)" = refs/heads/work
test "$(exec_p "$work_name" cat README)" = first
exec_p "$work_name" git remote -v | grep -F 'ssh://git@p/assembly' >/dev/null
exec_p "$work_name" git config user.name P
exec_p "$work_name" git config user.email p@example.invalid
exec_p "$work_name" sh -c 'printf "second\n" > second.txt'
exec_p "$work_name" git add second.txt
exec_p "$work_name" git commit -qm second
exec_p "$work_name" git push origin HEAD:work
test "$(inc file pull "$blank_name/workspace/README" -)" = first

# A dangling link is present for HEAD, but GET cannot read a regular file.
# Completing the runtime must refuse and leave the link untouched.
inc exec "$work_name" -- rm /etc/p/assets/p-attach
inc exec "$work_name" -- ln -s /missing-p-attach /etc/p/assets/p-attach
inc exec "$work_name" -- test -L /etc/p/assets/p-attach
inc exec "$work_name" -- test ! -e /missing-p-attach
runtime stop "$work_spec" | jq -e '.state=="stopped"' >/dev/null
expect_failure runtime create "$work_spec" "$work_at"
grep -Eq 'symbolic link|HEAD status|GET status|changed|differs from trusted assembly' "$step_dir/refused.err"
inc file pull -P "$work_name/etc/p/assets/p-attach" "$step_dir/p-attach-after-refusal"
test -L "$step_dir/p-attach-after-refusal"
test "$(readlink "$step_dir/p-attach-after-refusal")" = /missing-p-attach
echo P_RUNTIME_ASSEMBLY_PASS

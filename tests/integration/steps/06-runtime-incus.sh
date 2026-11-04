# shellcheck shell=bash
# Test-only caller supplies synthetic identities to the production Incus
# runtime driver. This does not implement or claim public session lifecycle.
set -E
umask 077
step_dir="$P_TEST_TMP/step-06"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
endpoint_prefix=/var/lib/p-vm/endpoints/pdev
name=
pids=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
cleanup() {
  if test -n "$name"; then inc delete --force "$name" >/dev/null 2>&1 || true; fi
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  if test -n "$name"; then rm -rf -- "${endpoint_prefix:?}/${name:?}"; fi
}
trap cleanup EXIT
trap 'printf "P_RUNTIME_INCUS_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2; tail -c 2048 "$step_dir/refused.err" >&2 || true; tail -c 1024 "$step_dir/git.err" >&2 || true' ERR
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/server"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/session"
# The session host clones its workspace from P's Git endpoint before it is
# ready, so the endpoint reaches a real Git fixture.
mkdir -m 0700 "$step_dir/git-state"
git-fixture serve "$step_dir/git-state" 127.0.0.1:0 "$step_dir/server" \
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
fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"incus"}' | jq -e '.ok' >/dev/null
seeded=$(fixture "$(jq -nc --arg key "$step_dir/session.pub" \
  '{schema:"p.git-fixture/v1",kind:"seed-session",key:"incus-main",project:"incus",branch:"main",choice:"blank",public_key:$key}')")
session=$(jq -er '.result.session_uuid' <<< "$seeded")
name="p-$session"
endpoint="$endpoint_prefix/$name"
mkdir -m 0755 "$endpoint"
socat "UNIX-LISTEN:$endpoint/session.sock,fork,mode=666" EXEC:cat & pids+=("$!")
socat "UNIX-LISTEN:$endpoint/git.sock,fork,mode=666" "TCP:$git_address" & pids+=("$!")
for ((attempt=0; attempt<100; attempt++)); do
  if test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"; then break; fi
  sleep 0.1
done
test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"
jq -n --arg session "$session" --arg image "$fingerprint" --arg endpoints "$endpoint" \
  --arg key "$step_dir/session" --arg server "$step_dir/server.pub" \
  '{instance_uuid:"11111111-1111-4111-8111-111111111111",session_uuid:$session,project:"incus",
    branch:"main",image:$image,endpoints:$endpoints,
    git_key_file:$key,git_host_key_file:$server}' > "$step_dir/session.json"
runtime() { timeout 190 runtime-fixture "$@"; }
invoke() { runtime "$1" "$step_dir/session.json" "${@:2}"; }
expect_failure() {
  if "$@" > "$step_dir/refused.out" 2> "$step_dir/refused.err"; then
    printf 'Unexpected runtime acceptance: %s\n' "$*" >&2
    return 1
  fi
}
instances() { inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length'; }
status() { inc list "$name" --format json | jq -r --arg n "$name" '.[]|select(.name==$n)|.status'; }

invoke inspect | jq -e '.state=="absent"' >/dev/null
# A fresh create calls its gate and leaves the installed runtime stopped. A
# second fresh create never adopts it; a create at its incarnation completes
# it and changes nothing.
created=$(invoke create)
jq -e '.state=="stopped" and .complete and .gated and (.incarnation|length>0)' <<< "$created" >/dev/null
at=$(jq -er '.incarnation' <<< "$created")
expect_failure invoke create
invoke create "$at" | jq -e --arg at "$at" '.state=="stopped" and .complete and .incarnation==$at and (.gated|not)' >/dev/null
test "$(instances)" -eq 1
inc list "$name" --format json | jq -e --arg n "$name" --arg endpoint "$endpoint" \
  '.[]|select(.name==$n)|.expanded_devices["p-endpoint"]|.source==$endpoint and .path=="/opt/p/endpoints" and .readonly=="true" and .shift=="false" and .propagation=="private"' >/dev/null
# Start returns once the session host is ready; again, it only observes that.
invoke start | jq -e --arg at "$at" '.state=="running" and .host.ready and .incarnation==$at' >/dev/null
invoke start | jq -e --arg at "$at" '.state=="running" and .host.ready and .incarnation==$at' >/dev/null
inc exec "$name" -- cat /proc/self/mountinfo | awk '
  $5 == "/opt/p/endpoints" {
    count++
    if ($6 !~ /(^|,)ro(,|$)/) bad=1
  }
  END { exit !(count == 1 && !bad) }
'
for socket in session git; do
  test "$(inc exec "$name" -- stat -c '%d:%i' "/opt/p/endpoints/$socket.sock")" = "$(stat -c '%d:%i' "$endpoint/$socket.sock")"
done
invoke stop | jq -e --arg at "$at" '.state=="stopped" and .incarnation==$at' >/dev/null
invoke stop | jq -e --arg at "$at" '.state=="stopped" and .incarnation==$at' >/dev/null
invoke inspect | jq -e --arg at "$at" '.state=="stopped" and .incarnation==$at and (.host.ready|not)' >/dev/null

# A runtime whose identity changed is never deleted or started.
inc config set "$name" user.p.session_uuid=00000000-0000-4000-8000-000000000000
expect_failure invoke delete "$at"
expect_failure invoke start
test "$(instances)" -eq 1
test "$(status)" = Stopped
inc config set "$name" "user.p.session_uuid=$session"

# Incus permits this source path, but the driver's closed instance plan does not.
inc config device add "$name" extra disk "source=$endpoint" path=/mnt/extra readonly=true shift=false
expect_failure invoke start
test "$(status)" = Stopped
inc config device remove "$name" extra
chmod 0600 "$endpoint/git.sock"
expect_failure invoke start
test "$(status)" = Stopped
chmod 0666 "$endpoint/git.sock"

jq '.socket="/var/lib/incus/unix.socket"' "$step_dir/session.json" > "$step_dir/admin.json"
expect_failure runtime inspect "$step_dir/admin.json"
jq '.socket="/var/lib/incus/missing/unix.socket.user"' "$step_dir/session.json" > "$step_dir/missing-socket.json"
expect_failure runtime inspect "$step_dir/missing-socket.json"
invoke start | jq -e '.state=="running" and .host.ready' >/dev/null
invoke stop | jq -e '.state=="stopped"' >/dev/null
invoke delete "$at" | jq -e '.state=="absent"' >/dev/null
invoke delete "$at" | jq -e '.state=="absent"' >/dev/null
test "$(instances)" -eq 0
echo P_RUNTIME_INCUS_PASS

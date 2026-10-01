# shellcheck shell=bash
# Test-only caller supplies synthetic identities to the production runtime
# broker. This does not implement or claim public session lifecycle.
set -E
umask 077
step_dir="$P_TEST_TMP/step-06"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
session=$(cat /proc/sys/kernel/random/uuid)
name="p-$session"
endpoint="/var/lib/p-vm/endpoints/pdev/$name"
mkdir -m 0755 "$endpoint"
pids=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
cleanup() {
  inc delete --force "$name" >/dev/null 2>&1 || true
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  rm -rf -- "${endpoint:?}"
}
trap cleanup EXIT
trap 'printf "P_RUNTIME_INCUS_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2; tail -c 2048 "$step_dir/refused.err" >&2 || true' ERR
for socket in session git; do
  socat "UNIX-LISTEN:$endpoint/$socket.sock,fork,mode=666" EXEC:cat &
  pids+=("$!")
done
for ((attempt=0; attempt<100; attempt++)); do
  if test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"; then break; fi
  sleep 0.1
done
test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"
activate_package() {
  local package="$1" target="$2" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["runtime.incus"],config:{}}]}' > "$target"
}
activate_package "$P_TEST_RUNTIME_PLUGIN" "$step_dir/provider.json"
activate_package "$P_TEST_RUNTIME_HOSTILE" "$step_dir/hostile.json"
jq -n --arg session "$session" --arg image "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")" --arg endpoint "$endpoint" \
  '{instance_uuid:"11111111-1111-4111-8111-111111111111",session_uuid:$session,image:$image,endpoint:$endpoint}' > "$step_dir/request.json"
invoke() { timeout 130 runtime-fixture "$step_dir/provider.json" "$1" "$step_dir/request.json"; }
expect_failure() {
  if "$@" > "$step_dir/refused.out" 2> "$step_dir/refused.err"; then
    printf 'Unexpected runtime acceptance: %s\n' "$*" >&2
    return 1
  fi
}
invoke runtime.inspect | jq -e '.exists==false' >/dev/null
invoke runtime.create | jq -e '.exists and .status=="Stopped"' >/dev/null
invoke runtime.create | jq -e '.exists and .status=="Stopped"' >/dev/null
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 1
inc list "$name" --format json | jq -e --arg n "$name" --arg endpoint "$endpoint" \
  '.[]|select(.name==$n)|.expanded_devices["p-endpoint"]|.source==$endpoint and .path=="/opt/p/endpoints" and .readonly=="true" and .shift=="false" and .propagation=="private"' >/dev/null
invoke runtime.start | jq -e '.exists and .status=="Running"' >/dev/null
invoke runtime.start | jq -e '.exists and .status=="Running"' >/dev/null
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
invoke runtime.stop | jq -e '.exists and .status=="Stopped"' >/dev/null

# A package asked only to inspect cannot delete this real stopped instance.
expect_failure runtime-fixture "$step_dir/hostile.json" runtime.inspect "$step_dir/request.json"
invoke runtime.inspect | jq -e '.exists and .status=="Stopped"' >/dev/null

inc config set "$name" user.p.session_uuid=00000000-0000-4000-8000-000000000000
expect_failure invoke runtime.delete
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 1
inc config set "$name" "user.p.session_uuid=$session"

# Incus permits this source path, but the core's closed instance plan does not.
inc config device add "$name" extra disk "source=$endpoint" path=/mnt/extra readonly=true shift=false
expect_failure invoke runtime.start
test "$(inc list "$name" --format json | jq -r --arg n "$name" '.[]|select(.name==$n)|.status')" = Stopped
inc config device remove "$name" extra
chmod 0600 "$endpoint/git.sock"
expect_failure invoke runtime.start
test "$(inc list "$name" --format json | jq -r --arg n "$name" '.[]|select(.name==$n)|.status')" = Stopped
chmod 0666 "$endpoint/git.sock"

jq '.socket="/var/lib/incus/unix.socket"' "$step_dir/request.json" > "$step_dir/admin.json"
expect_failure runtime-fixture "$step_dir/provider.json" runtime.inspect "$step_dir/admin.json"
jq '.socket="/var/lib/incus/missing/unix.socket.user"' "$step_dir/request.json" > "$step_dir/missing-socket.json"
expect_failure runtime-fixture "$step_dir/provider.json" runtime.inspect "$step_dir/missing-socket.json"
invoke runtime.start | jq -e '.exists and .status=="Running"' >/dev/null
invoke runtime.stop | jq -e '.exists and .status=="Stopped"' >/dev/null
invoke runtime.delete | jq -e '.exists==false' >/dev/null
invoke runtime.delete | jq -e '.exists==false' >/dev/null
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 0
echo P_RUNTIME_INCUS_PASS

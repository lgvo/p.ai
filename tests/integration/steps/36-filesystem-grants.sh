# shellcheck shell=bash
# Trusted typed filesystem grants against confined Incus and real host paths.
set -E
umask 077
step_dir="$P_TEST_TMP/step-36"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step36
grant_root=/var/lib/p-vm/grants/pdev/step36
mkdir -m 0700 "$endpoint_prefix" "$grant_root"
mkdir -m 0755 "$grant_root/ro" "$grant_root/rw"
chmod 0777 "$grant_root/rw"
printf read-only > "$grant_root/ro/readme"
chmod 0644 "$grant_root/ro/readme"
cat > "$grant_root/ro/probe.sh" <<'EOF'
#!/run/current-system/sw/bin/bash
printf unexpected
EOF
cat > "$grant_root/rw/run.sh" <<'EOF'
#!/run/current-system/sw/bin/bash
printf allowed
EOF
cat > "$grant_root/notice" <<'EOF'
#!/run/current-system/sw/bin/bash
printf file-grant
EOF
chmod 0755 "$grant_root/ro/probe.sh" "$grant_root/rw/run.sh" "$grant_root/notice"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid_a=
uuid_b=
uuid_a2=
incus_real=$(command -v incus)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
guest_a() {
  inc exec "p-$uuid_a" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_daemon() {
  if test -n "$daemon_pid"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((i=0; i<100; i++)); do
      if ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$daemon_pid" 2>/dev/null; then kill -KILL "$daemon_pid" 2>/dev/null || true; fi
    wait "$daemon_pid" 2>/dev/null || true
    daemon_pid=
  fi
}
cleanup() {
  stop_daemon
  for id in "$uuid_a" "$uuid_b" "$uuid_a2"; do
    if test -n "$id"; then
      inc delete --force "p-$id" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/$id"
    fi
  done
  rm -rf -- "$grant_root"
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_FILESYSTEM_GRANTS_FAIL line=%s status=%s\n' "$2" "$1" >&2
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(length==64)')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install
base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_real")
jq -n --arg state "$state" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg host_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  --arg ro "$grant_root/ro" --arg rw "$grant_root/rw" --arg file "$grant_root/notice" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
    runtime:{activation_path:$runtime_activation,
      host_plugin_id:$host_id,driver:$driver,
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policies:{
        "grant-a":{network:"none",command:["/run/current-system/sw/bin/bash"],
          filesystem_mounts:[
            {name:"data",source:$ro,type:"directory",access:"read-only",executable:false},
            {name:"scratch",source:$rw,type:"directory",access:"read-write",executable:true},
            {name:"notice",source:$file,type:"file",access:"read-only",executable:false}
          ]},
        "grant-b":{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}
      }}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'grant daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'grant daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
  done
  echo "operation $id did not complete" >&2
  return 1
}
# change_session METHOD UUID: METHOD, session.start or session.stop, records
# an operation of its kind for session UUID, which completes.
change_session() {
  local id
  id=$(rpc "$1" "$(jq -nc --arg uuid "$2" '{uuid:$uuid}')" | jq -er --arg kind "$1" \
    '.result | select(.changed==true and .operation.kind==$kind) | .operation.id') || return
  wait_operation "$id"
}
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$id" '{uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
  return 1
}
# discard_owned UUID: discards session UUID, a dry run then a run with its
# change tag, and waits for the discard to complete.
discard_owned() {
  local id="$1" plan tag confirmed op
  plan=$(rpc session.discard "$(jq -nc --arg uuid "$id" '{uuid:$uuid}')")
  tag=$(jq -er --arg target "session/$id" '.result | select(.plan.allowed==true and .plan.target==$target) |
    .change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$plan")
  confirmed=$(rpc session.discard "$(jq -nc --arg uuid "$id" --arg tag "$tag" \
    '{uuid:$uuid,key:"grant-discard",if_match:$tag}')")
  op=$(jq -er '.result.operation.id | select(length==36)' <<< "$confirmed")
  wait_operation "$op"
}
start_daemon
created_a=$(rpc project.create '{"key":"grant-a-bootstrap","project":"grant-a"}')
op_a=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_a")
uuid_a=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_a")
wait_operation "$op_a"
wait_ready "$uuid_a"
test "$(guest_a /run/current-system/sw/bin/cat /mnt/p/data/readme)" = read-only
test "$(guest_a /run/current-system/sw/bin/cat /mnt/p/notice)" = '#!/run/current-system/sw/bin/bash'$'\n''printf file-grant'
if guest_a /run/current-system/sw/bin/bash -c 'printf denied > /mnt/p/data/new' > "$step_dir/ro-write.out" 2>&1; then
  echo 'read-only grant accepted write' >&2
  exit 1
fi
if guest_a /mnt/p/data/probe.sh > "$step_dir/noexec.out" 2>&1; then
  echo 'non-executable grant accepted execution' >&2
  exit 1
fi
if guest_a /run/current-system/sw/bin/bash -c 'printf denied > /mnt/p/notice' > "$step_dir/file-ro.out" 2>&1; then
  echo 'read-only file grant accepted write' >&2
  exit 1
fi
if guest_a /mnt/p/notice > "$step_dir/file-noexec.out" 2>&1; then
  echo 'non-executable file grant accepted execution' >&2
  exit 1
fi
test "$(guest_a /mnt/p/scratch/run.sh)" = allowed
guest_a /run/current-system/sw/bin/bash -c 'printf guest-write > /mnt/p/scratch/guest-file'
test "$(cat "$grant_root/rw/guest-file")" = guest-write
guest_a /run/current-system/sw/bin/git config user.name P
guest_a /run/current-system/sw/bin/git config user.email p@example.invalid
guest_a /run/current-system/sw/bin/bash -c \
  'printf main > tracked; git add tracked; git commit -qm main; git push origin HEAD:main'

created_b=$(rpc project.create '{"key":"grant-b-bootstrap","project":"grant-b"}')
op_b=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$op_b"
wait_ready "$uuid_b"
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'test ! -e /mnt/p/data && test ! -L /mnt/p/data && test ! -e /mnt/p/scratch && test ! -L /mnt/p/scratch && test ! -e /mnt/p/notice && test ! -L /mnt/p/notice'
discard_owned "$uuid_a"
test "$(cat "$grant_root/ro/readme")" = read-only
test "$(cat "$grant_root/rw/guest-file")" = guest-write
test -f "$grant_root/notice"

created_a2=$(rpc session.create '{"key":"grant-a-recreate","project":"grant-a","branch":"main","choice":"existing"}')
op_a2=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_a2")
uuid_a2=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_a2")
wait_operation "$op_a2"
wait_ready "$uuid_a2"
change_session session.stop "$uuid_a2"
rpc session.inspect "$(jq -nc --arg uuid "$uuid_a2" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
mv -- "$grant_root/ro" "$grant_root/ro-old"
mkdir -m 0755 "$grant_root/ro"
printf replacement > "$grant_root/ro/readme"
rpc session.inspect "$(jq -nc --arg uuid "$uuid_a2" '{uuid:$uuid}')" |
  jq -e '.result.session.policy_condition=="invalid"' >/dev/null
if rpc session.start "$(jq -nc --arg uuid "$uuid_a2" '{uuid:$uuid}')" > "$step_dir/swapped-start.out" 2>&1; then
  echo 'Start accepted swapped grant source' >&2
  exit 1
fi
inc list "^p-$uuid_a2$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
echo P_FILESYSTEM_GRANTS_PASS

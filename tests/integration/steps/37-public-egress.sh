# shellcheck shell=bash
# shellcheck disable=SC2016 # Guest-side expansions must stay literal on the host.
# Public-egress confinement and packet behavior on a dedicated managed bridge.
set -E
umask 077
step_dir="$P_TEST_TMP/step-37"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step37
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
sibling_pid=
uuid_a=
uuid_b=
uuid_new=
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
  if test -n "$sibling_pid"; then kill "$sibling_pid" 2>/dev/null || true; wait "$sibling_pid" 2>/dev/null || true; fi
  for id in "$uuid_a" "$uuid_b" "$uuid_new"; do
    if test -n "$id"; then
      inc delete --force "p-$id" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/$id"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_PUBLIC_EGRESS_FAIL line=%s status=%s\n' "$2" "$1" >&2
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
  if inc network show p-public-v1 > "$step_dir/network-show.out" 2>&1; then
    echo 'confined Incus bridge configuration:' >&2
    tail -c 3072 "$step_dir/network-show.out" >&2 || true
  else
    echo 'confined Incus bridge inspection failed:' >&2
    tail -c 1024 "$step_dir/network-show.out" >&2 || true
  fi
  if inc network acl show p-public-v1-acl > "$step_dir/acl-show.out" 2>&1; then
    echo 'confined Incus ACL configuration:' >&2
    tail -c 4096 "$step_dir/acl-show.out" >&2 || true
  else
    echo 'confined Incus ACL inspection failed:' >&2
    tail -c 1024 "$step_dir/acl-show.out" >&2 || true
  fi
  if test -n "$uuid_a"; then
    echo 'public guest network inventory after failure:' >&2
    if inc exec "p-$uuid_a" -- /run/current-system/sw/bin/ip -j address show dev eth0 \
      > "$step_dir/guest-address.out" 2>&1; then
      tail -c 2048 "$step_dir/guest-address.out" >&2 || true
    else
      tail -c 1024 "$step_dir/guest-address.out" >&2 || true
    fi
    if inc exec "p-$uuid_a" -- /run/current-system/sw/bin/ip -j -4 route show \
      > "$step_dir/guest-route.out" 2>&1; then
      tail -c 2048 "$step_dir/guest-route.out" >&2 || true
    else
      tail -c 1024 "$step_dir/guest-route.out" >&2 || true
    fi
  fi
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
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host-one.json" session.asset.install
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"
base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policies:{
        "policy-a":{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]},
        "policy-b":{network:"none",command:["/run/current-system/sw/bin/bash"]}
      }}}' > "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'policy daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'policy daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not complete" >&2
  return 1
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if inspect "$id" | jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
  return 1
}
update_config() {
  local filter="$1"
  jq "$filter" "$step_dir/host.json" > "$step_dir/host.next"
  mv -- "$step_dir/host.next" "$step_dir/host.json"
}
jq --arg nft "$P_TEST_NFT_BINARY" --arg proof "$P_TEST_NETWORK_PROOF_BINARY" '
  .runtime.public_egress={network:"p-public-v1",acl:"p-public-v1-acl",
    bridge_ipv4:"10.233.0.1/24",dns:["1.1.1.1","9.9.9.9"],
    sudo_binary:"/run/wrappers/bin/sudo",nft_binary:$nft,
    bridge_proof_binary:$proof} |
  .runtime.project_policies["policy-a"].network="public-egress" |
  .runtime.project_policies["policy-c"]={network:"public-egress",
    filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}
' "$step_dir/host.json" > "$step_dir/host.next"
mv -- "$step_dir/host.next" "$step_dir/host.json"
start_daemon
created_a=$(rpc project.create '{"v":1,"key":"egress-a-bootstrap","project":"policy-a"}')
op_a=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_a")
uuid_a=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_a")
wait_operation "$op_a"
wait_ready "$uuid_a"
created_b=$(rpc project.create '{"v":1,"key":"egress-b-bootstrap","project":"policy-b"}')
op_b=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$op_b"
wait_ready "$uuid_b"
created_c=$(rpc project.create '{"v":1,"key":"egress-c-bootstrap","project":"policy-c"}')
op_c=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_c")
uuid_new=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_c")
wait_operation "$op_c"
wait_ready "$uuid_new"

inc list "^p-$uuid_a$" --format json |
  jq -e 'length==1 and ([.[0].devices[] | select(.type=="nic" and .network=="p-public-v1" and ."ipv4.address"=="10.233.0.10")] | length)==1' >/dev/null
inc list "^p-$uuid_b$" --format json |
  jq -e 'length==1 and ([.[0].devices[] | select(.type=="nic")] | length)==0' >/dev/null
inc list "^p-$uuid_new$" --format json |
  jq -e 'length==1 and ([.[0].devices[] | select(.type=="nic" and ."ipv4.address"=="10.233.0.11")] | length)==1' >/dev/null
guest_a /run/current-system/sw/bin/bash -c '
  ip -4 -o addr show dev eth0 | grep -q "10.233.0.10/24" &&
  ip -4 route show default | grep -q "via 10.233.0.1" &&
  grep -qx "nameserver 1.1.1.1" /etc/resolv.conf &&
  grep -qx "nameserver 9.9.9.9" /etc/resolv.conf'
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'test ! -e /sys/class/net/eth0'

# A root-owned live host listener uses allowed port 443, distinguishing
# destination filtering from the ACL's default service-port refusal.
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if /run/current-system/sw/bin/python3 -c 'import socket; s=socket.create_connection(("10.233.0.1",443),1); s.close()' 2>/dev/null; then break; fi
  sleep 0.1
done
/run/current-system/sw/bin/python3 -c 'import socket; s=socket.create_connection(("10.233.0.1",443),1); s.close()'
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 4 bash -c "exec 3<>/dev/tcp/10.233.0.1/443"' > "$step_dir/gateway-probe.out" 2>&1; then
  echo 'public session reached gateway listener' >&2
  exit 1
fi
/run/current-system/sw/bin/python3 -c 'import socket; s=socket.create_connection(("8.8.8.8",443),1); s.close()'
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 4 bash -c "exec 3<>/dev/tcp/8.8.8.8/443"' > "$step_dir/host-public-probe.out" 2>&1; then
  echo 'public session reached host public address' >&2
  exit 1
fi
for forbidden in 100.64.0.1 169.254.169.254 172.16.0.1 192.168.1.1; do
  if guest_a /run/current-system/sw/bin/bash -c \
    'timeout 3 bash -c "exec 3<>/dev/tcp/$1/443"' bash "$forbidden" \
    > "$step_dir/forbidden-${forbidden}.out" 2>&1; then
    echo "public session reached forbidden address $forbidden" >&2
    exit 1
  fi
done
guest_a /run/current-system/sw/bin/bash -c \
  'test -z "$(ip -6 route show default)" && test -z "$(ip -6 -o addr show dev eth0 scope global)"'
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 3 bash -c "exec 3<>/dev/tcp/::ffff:8.8.8.8/443"' \
  > "$step_dir/mapped-v6-probe.out" 2>&1; then
  echo 'public session reached host through IPv4-mapped IPv6' >&2
  exit 1
fi

# The second public guest provides a live sibling destination.
inc exec "p-$uuid_new" -- \
  /run/current-system/sw/bin/python3 -m http.server 443 --bind 10.233.0.11 \
  > "$step_dir/sibling.out" 2>&1 &
sibling_pid=$!
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if inc exec "p-$uuid_new" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
    /run/current-system/sw/bin/bash -c 'timeout 2 bash -c "exec 3<>/dev/tcp/10.233.0.11/443"' \
    >/dev/null 2>&1; then break; fi
  sleep 0.1
done
inc exec "p-$uuid_new" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'timeout 2 bash -c "exec 3<>/dev/tcp/10.233.0.11/443"' >/dev/null
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 4 bash -c "exec 3<>/dev/tcp/10.233.0.11/443"' > "$step_dir/sibling-probe.out" 2>&1; then
  echo 'public session reached sibling listener' >&2
  exit 1
fi
test "$(cat /proc/sys/net/ipv4/ip_forward)" = 1
/run/current-system/sw/bin/ip -4 route get 8.8.4.10 | grep -F 'dev p-vm-dnat-host' >/dev/null
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if /run/current-system/sw/bin/python3 -c 'import socket; s=socket.create_connection(("8.8.4.10",443),1); s.close()' 2>/dev/null; then break; fi
  sleep 0.1
done
/run/current-system/sw/bin/python3 -c 'import socket; s=socket.create_connection(("8.8.4.10",443),1); s.close()'
dnat_packets() {
  local name="$1" observed
  case "$name" in prerouting_hits|forward_hits|forward_any_hits|input_any_hits) ;; *) return 1 ;; esac
  observed=$(/run/wrappers/bin/sudo -n "$P_TEST_NFT_BINARY" \
    list counter ip p_vm_dnat_probe "$name") || return 1
  [[ "$observed" =~ packets[[:space:]]+([0-9]+) ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1]}"
}
production_dnat_packets() {
  /run/wrappers/bin/sudo -n "$P_TEST_NFT_BINARY" -j list table inet p_egress_p_public_v1 |
    jq -er '[.nftables[] | .rule? | select(.table=="p_egress_p_public_v1" and .chain=="forward") | .expr] |
      .[0][2].counter.packets | select(type=="number" and .>=0)'
}
before_prerouting=$(dnat_packets prerouting_hits)
before_forward=$(dnat_packets forward_hits)
before_forward_any=$(dnat_packets forward_any_hits)
before_input_any=$(dnat_packets input_any_hits)
before_production=$(production_dnat_packets)
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 4 bash -c "exec 3<>/dev/tcp/8.8.4.4/443"' > "$step_dir/dnat-probe.out" 2>&1; then
  echo 'public session reached disposable public DNAT target' >&2
  exit 1
fi
after_prerouting=$(dnat_packets prerouting_hits)
after_forward=$(dnat_packets forward_hits)
after_forward_any=$(dnat_packets forward_any_hits)
after_input_any=$(dnat_packets input_any_hits)
after_production=$(production_dnat_packets)
if (( after_prerouting <= before_prerouting || after_forward <= before_forward || after_production <= before_production )); then
  echo "DNAT path was not proved: pre=$before_prerouting/$after_prerouting forward=$before_forward/$after_forward production=$before_production/$after_production forward_any=$before_forward_any/$after_forward_any input_any=$before_input_any/$after_input_any" >&2
  exit 1
fi
echo P_PUBLIC_DNAT_NEGATIVE_PASS
# A deterministic resolver answer models a hostname changing to a forbidden
# address. This proves the packet filter acts on the resolved destination; it
# is not evidence of an actual public DNS exchange or HTTP redirect.
if ! guest_a /run/current-system/sw/bin/python3 -c '
import socket
import sys

name = "rebound.example.invalid"
original = socket.getaddrinfo
for address in ("10.233.0.1", "10.233.0.11"):
    def answer(host, port, *args, **kwargs):
        if host == name:
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (address, port))]
        return original(host, port, *args, **kwargs)
    socket.getaddrinfo = answer
    if socket.getaddrinfo(name, 443)[0][4][0] != address:
        sys.exit("synthetic resolver answer changed")
    try:
        connection = socket.create_connection((name, 443), timeout=3)
    except OSError:
        continue
    connection.close()
    sys.exit("synthetic rebinding reached forbidden destination")
' > "$step_dir/synthetic-rebind.out" 2>&1; then
  echo 'synthetic hostname-to-private-address probe failed' >&2
  tail -c 1024 "$step_dir/synthetic-rebind.out" >&2 || true
  exit 1
fi
echo P_PUBLIC_SYNTHETIC_RESOLUTION_NEGATIVE_PASS
kill "$sibling_pid" 2>/dev/null || true
wait "$sibling_pid" 2>/dev/null || true

# Positive fetch is separate evidence; an isolated VM may have no public route.
if guest_a /run/current-system/sw/bin/bash -c \
  'timeout 20 nix store prefetch-file https://example.com/' > "$step_dir/fetch.out" 2>&1; then
  echo P_PUBLIC_NIX_FETCH_PASS
else
  echo P_PUBLIC_NIX_FETCH_UNVERIFIED
fi
echo P_PUBLIC_EGRESS_NEGATIVE_PASS

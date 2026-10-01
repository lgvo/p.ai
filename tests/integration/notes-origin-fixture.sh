#!/usr/bin/env bash
# Manual notes-lab origin setup/observation only; never navigate P or publish.
set -euo pipefail
umask 077
fail() { echo "notes-origin-fixture: $*" >&2; exit 1; }
test "$(id -un)" = pdev || fail 'run as the outer notes-lab owner pdev'
owner_home=$(getent passwd "$(id -u)" | cut -d: -f6)
test "$owner_home" = /home/pdev && test "$HOME" = "$owner_home" || fail 'unexpected lab owner home'
state=/var/lib/p-demo/notes-origin-review
ssh_dir="$owner_home/.ssh"
ssh_config="$ssh_dir/config"
mode=${1-}
for binary in git jq ssh-keygen sha256sum cmp; do command -v "$binary" >/dev/null || fail "missing $binary"; done

validate_record() {
  test -d "$state" && test ! -L "$state" || fail 'fixture directory absent or replaced'
  test "$(stat -c %u "$state")" = "$(id -u)" || fail 'fixture owner changed'
  jq -e --arg state "$state" --argjson uid "$(id -u)" \
    '.schema=="p.notes-origin-fixture/v1" and .state==$state and .uid==$uid' "$state/record.json" >/dev/null \
    || fail 'fixture record identity changed'
  test -f "$ssh_config" && test ! -L "$ssh_config" && cmp -s "$ssh_config" "$state/ssh-config" \
    || fail 'owner SSH config changed; refusing cleanup or verification'
}

server_identity() {
  local pid=$1 start=$2 binary=$3
  test -r "/proc/$pid/stat" && test "$(stat -c %u "/proc/$pid")" = "$(id -u)" || return 1
  test "$(awk '{print $22}' "/proc/$pid/stat")" = "$start" || return 1
  # Every argument must match: no PID-only cleanup of an unrelated process.
  cmp -s <(printf '%s\0' "$binary" serve-publish "$state/host-key" "$state/client-key.pub" \
    "$state/empty.git" "$state/full.git" "$state/empty.git" "$state/server.trace" "$state/never-drop-response") \
    "/proc/$pid/cmdline"
}

case "$mode" in
  setup)
    test "$#" = 1 || fail 'setup takes no arguments'
    command -v origin-fixture >/dev/null || fail 'notes lab must provision origin-fixture'
    test -d /etc/p-notes-example || fail 'notes sample is absent'
    test ! -e "$state" && test ! -L "$state" || fail 'existing fixture must be cleaned explicitly'
    test ! -e "$ssh_config" && test ! -L "$ssh_config" || fail 'existing owner SSH config is preserved'
    test ! -L "$ssh_dir" || fail 'owner SSH directory is a symlink'
    test -d /var/lib/p-demo && test ! -L /var/lib/p-demo \
      && test "$(stat -c %u /var/lib/p-demo)" = "$(id -u)" \
      && test "$(stat -c %a /var/lib/p-demo)" = 700 \
      || fail 'expected the private notes-lab daemon state directory'
    mkdir -m 0700 "$state"
    created_config=false
    created_ssh=false
    origin_pid=
    setup_complete=false
    rollback() {
      if ! "$setup_complete"; then
        if test -n "$origin_pid" && server_identity "$origin_pid" "$origin_start" "$origin_binary"; then kill "$origin_pid"; fi
        if "$created_config" && cmp -s "$ssh_config" "$state/ssh-config"; then rm -- "$ssh_config"; fi
        rm -rf -- "$state"
        if "$created_ssh"; then rmdir "$ssh_dir" 2>/dev/null || true; fi
      fi
    }
    trap rollback EXIT
    if test ! -d "$ssh_dir"; then mkdir -m 0700 "$ssh_dir"; created_ssh=true; fi
    test "$(stat -c %u "$ssh_dir")" = "$(id -u)" || fail 'owner SSH directory owner differs'
    mkdir "$state/seed"
    mkdir -p "$state/seed/examples/notes"
    cp -R /etc/p-notes-example/. "$state/seed/examples/notes/"
    chmod -R u+w "$state/seed"
    git init -q -b main "$state/seed"
    git -C "$state/seed" add examples/notes
    git -C "$state/seed" -c user.name=P -c user.email=p@example.invalid commit -qm 'Seed the exact notes example'
    source_oid=$(git -C "$state/seed" rev-parse HEAD)
    git init --bare -q -b main "$state/full.git"
    git init --bare -q -b main "$state/empty.git"
    git -C "$state/seed" push -q "$state/full.git" HEAD:refs/heads/main
    ssh-keygen -q -t ed25519 -N '' -f "$state/host-key"
    ssh-keygen -q -t ed25519 -N '' -f "$state/client-key"
    origin_binary=$(readlink -f "$(command -v origin-fixture)")
    nohup "$origin_binary" serve-publish "$state/host-key" "$state/client-key.pub" \
      "$state/empty.git" "$state/full.git" "$state/empty.git" "$state/server.trace" "$state/never-drop-response" \
      < /dev/null > "$state/server-ready.json" 2> "$state/server.err" &
    origin_pid=$!
    origin_start=$(awk '{print $22}' "/proc/$origin_pid/stat")
    for ((attempt=0; attempt<100; attempt++)); do
      if jq -e '.address | type=="string"' "$state/server-ready.json" >/dev/null 2>&1; then break; fi
      kill -0 "$origin_pid" 2>/dev/null || fail 'fixture server exited'
      sleep .1
    done
    port=$(jq -er '.address | select(startswith("127.0.0.1:")) | split(":")[-1] | tonumber' "$state/server-ready.json")
    awk '{print "p-notes-review-origin " $1 " " $2}' "$state/host-key.pub" > "$state/known-hosts"
    cat > "$state/ssh-config" <<CONFIG
Host notes-review-origin
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias p-notes-review-origin
  UserKnownHostsFile $state/known-hosts
  StrictHostKeyChecking yes
  IdentityFile $state/client-key
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 2
CONFIG
    # noclobber also refuses a config created while the fixture was starting.
    (set -C; cat "$state/ssh-config" > "$ssh_config")
    created_config=true
    jq -n --arg state "$state" --argjson uid "$(id -u)" --argjson port "$port" \
      --argjson pid "$origin_pid" --arg start "$origin_start" --arg binary "$origin_binary" \
      --arg source_oid "$source_oid" --argjson created_ssh "$created_ssh" \
      '{schema:"p.notes-origin-fixture/v1",state:$state,uid:$uid,port:$port,pid:$pid,start:$start,binary:$binary,
        source_oid:$source_oid,source_oid_path:($state+"/source-oid"),created_ssh:$created_ssh,
        url:"pdev@notes-review-origin:full.git",empty_url:"ssh://pdev@notes-review-origin/empty.git"}' \
      > "$state/record.json"
    printf '%s\n' "$source_oid" > "$state/source-oid"
    test "$(git ls-remote pdev@notes-review-origin:full.git refs/heads/main | cut -f1)" = "$source_oid"
    setup_complete=true
    cat "$state/record.json"
    ;;
  verify)
    test "$#" = 4 || fail 'verify PROJECT REF EXPECTED_OID'
    validate_record
    project=$2; ref=$3; expected=$4
    git check-ref-format "$ref"
    [[ $ref == refs/heads/* && $expected =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]] || fail 'expected an exact branch ref and object ID'
    remote=$(git ls-remote pdev@notes-review-origin:full.git "$ref" | awk -v ref="$ref" '$2==ref {print $1}')
    local_remote=$(git -C "$state/full.git" rev-parse --verify "$ref")
    source=$(timeout 30 p api project.branches "$(jq -nc --arg project "$project" '{v:1,project:$project,limit:8}')" \
      | jq -er --arg ref "$ref" '.result.refs[] | select(.ref==$ref) | .oid')
    test "$remote" = "$expected" && test "$local_remote" = "$expected" && test "$source" = "$expected" \
      || fail 'published remote or canonical source differs from the exact expected OID'
    jq -nc --arg project "$project" --arg ref "$ref" --arg oid "$expected" \
      '{verified:true,project:$project,ref:$ref,source_oid:$oid,remote_oid:$oid}'
    ;;
  cleanup)
    test "$#" = 1 || fail 'cleanup takes no arguments'
    validate_record
    origin_pid=$(jq -er '.pid' "$state/record.json")
    origin_start=$(jq -er '.start' "$state/record.json")
    origin_binary=$(jq -er '.binary' "$state/record.json")
    created_ssh=$(jq -er '.created_ssh|tostring' "$state/record.json")
    if test -d "/proc/$origin_pid"; then
      server_identity "$origin_pid" "$origin_start" "$origin_binary" || fail 'server process identity changed; no cleanup applied'
      kill "$origin_pid"
      for ((attempt=0; attempt<100; attempt++)); do
        server_identity "$origin_pid" "$origin_start" "$origin_binary" || break
        sleep .1
      done
      ! server_identity "$origin_pid" "$origin_start" "$origin_binary" || fail 'server has not stopped'
    fi
    rm -- "$ssh_config"
    rm -rf -- "$state"
    if "$created_ssh"; then rmdir "$ssh_dir" 2>/dev/null || true; fi
    printf '{"cleaned":true}\n'
    ;;
  *) echo 'Usage: bash /etc/p-notes-origin-fixture.sh setup|verify PROJECT REF EXPECTED_OID|cleanup' >&2; exit 2 ;;
esac

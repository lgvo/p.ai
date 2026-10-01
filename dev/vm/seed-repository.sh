#!/usr/bin/env bash
# Lab provisioning through the real lifecycle and confined Incus APIs.
set -euo pipefail
umask 077
state=/var/lib/p-demo
export P_SOCKET="$state/control.sock"
record="$state/lab-repository.json"
if test -e "$record"; then exit 0; fi
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
rpc() { timeout 20 p api "$@"; }
inc() { timeout 120 incus --force-local --project user-1000 "$@"; }
deadline=$((SECONDS+600))
until rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; do
  if ((SECONDS >= deadline)); then echo 'P daemon did not become ready for repository seeding.' >&2; exit 1; fi
  sleep 1
done

# Pin the first bundle across interrupted seeding and changed VM builds.
mkdir -p "$state/lab-seed"
bundle="$state/lab-seed/repository.bundle"
if ! test -e "$bundle"; then
  cp "$P_LAB_REPOSITORY_BUNDLE" "$bundle.pending"
  mv -T "$bundle.pending" "$bundle"
fi
source_commit=$(git bundle list-heads "$bundle" HEAD | cut -d ' ' -f 1)
test -n "$source_commit"
created=$(rpc project.create '{"v":1,"key":"p-lab-repository-v1","project":"p-ai"}')
operation=$(jq -er '.result.operation.id' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
while :; do
  observed=$(rpc operation.inspect "$(jq -nc --arg id "$operation" '{v:1,id:$id}')")
  status=$(jq -er '.result.operation.status' <<< "$observed")
  if test "$status" = completed; then break; fi
  if test "$status" = blocked || test "$status" = failed || ((SECONDS >= deadline)); then
    jq -c '.result.operation|{id,status,phase,diagnostic}' <<< "$observed" >&2
    exit 1
  fi
  sleep 1
done
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
until rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null; do
  if ((SECONDS >= deadline)); then echo 'Repository session did not become ready.' >&2; exit 1; fi
  sleep 1
done
inc file push "$bundle" "p-$uuid/home/p/p-lab-repository.bundle" --uid 1000 --gid 1000 --mode 0600
inc file push "$P_LAB_REPOSITORY_LOADER" "p-$uuid/home/p/p-lab-load-repository.sh" --uid 1000 --gid 1000 --mode 0600
inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- \
  /run/current-system/sw/bin/bash /home/p/p-lab-load-repository.sh "$source_commit"
pending=$(mktemp "$state/lab-repository.XXXXXXXX")
trap 'rm -f -- "$pending"' EXIT
jq -n --arg uuid "$uuid" --arg source "$source_commit" \
  '{project:"p-ai",branch:"main",session_uuid:$uuid,source_commit:$source}' > "$pending"
mv -T "$pending" "$record"
echo "P_LAB_REPOSITORY_READY project=p-ai branch=main session=$uuid commit=$source_commit"

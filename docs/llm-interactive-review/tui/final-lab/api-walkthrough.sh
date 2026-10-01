#!/usr/bin/env bash
set -euo pipefail
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
rpc() { p api "$@"; }
inc() { timeout --kill-after=5 90 incus --force-local --project user-1000 "$@"; }
wait_op() {
 local id=$1 result status
 for attempt in {1..240}; do
  result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
  status=$(jq -r '.result.operation.status' <<<"$result")
  if test "$status" = completed; then printf '%s\n' "$result"; return; fi
  if test "$status" = failed || test "$status" = blocked; then printf '%s\n' "$result"; return 1; fi
  sleep .5
 done
 return 1
}
uuid=$(cat /tmp/notes-final-plumbing-uuid 2>/dev/null || true)
guest() { inc exec "p-$uuid" --force-noninteractive --disable-stdin --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh --env XDG_RUNTIME_DIR=/run/user/1000 --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"; }
case "$1" in
 create|resume-create)
  if test "$1" = create; then bash /etc/p-notes-origin-fixture.sh setup | tee /tmp/notes-origin-fixture.json; fi
  url=$(jq -r .url /tmp/notes-origin-fixture.json)
  oid=$(jq -r .source_oid /tmp/notes-origin-fixture.json)
  result=$(rpc project.create "$(jq -nc --arg url "$url" '{v:1,key:"notes-final-plumbing-project",project:"notes-final-plumbing",url:$url}')")
  if test "$1" = resume-create; then result=$(rpc operation.retry "$(jq -nc --arg id "$(jq -r .result.operation.id <<<"$result")" '{v:1,id:$id}')"); fi
  printf '%s\n' "$result"; wait_op "$(jq -r .result.operation.id <<<"$result")"
  result=$(rpc session.create "$(jq -nc --arg url "$url" --arg oid "$oid" '{v:1,key:"notes-final-plumbing-main",project:"notes-final-plumbing",branch:"main",choice:"new",origin_ref:"refs/heads/main",expected_commit_oid:$oid,expected_origin_url:$url}')")
  printf '%s\n' "$result"; uuid=$(jq -r .result.operation.session_uuid <<<"$result"); printf '%s\n' "$uuid" >/tmp/notes-final-plumbing-uuid
  wait_op "$(jq -r .result.operation.id <<<"$result")" | tee /tmp/notes-final-plumbing-create.json
  test "$(guest git rev-parse HEAD)" = "$oid"
  printf 'API_IMPORTED_SOURCE uuid=%s oid=%s\n' "$uuid" "$oid"
  ;;
 sample)
  guest python3 examples/notes/install.py
  guest python3 examples/notes/tests.py
  for unit in db web worker; do
   rpc session.service.action "$(jq -nc --arg uuid "$uuid" --arg unit "p-project-notes-$unit.service" '{v:1,uuid:$uuid,unit:$unit,action:"start"}')"
  done
  sleep 2
  guest python3 examples/notes/client.py add 'API imported sample durable row'
  sleep 2
  guest psql -h /home/p/.local/state/p-notes/socket -d notes -Atc 'select body,status,word_count from notes'
  guest bash -c 'printf "\nAPI origin review commit.\n" >> examples/notes/README.md; git add examples/notes/README.md; git -c user.name=P -c user.email=p@example.invalid commit -m "Review imported sample through API"; git push origin HEAD:refs/heads/main'
  oid=$(guest git rev-parse HEAD); printf '%s\n' "$oid" >/tmp/notes-final-plumbing-oid
  seed=$(jq -r .source_oid /tmp/notes-origin-fixture.json)
  remote=$(git ls-remote pdev@notes-review-origin:full.git refs/heads/main | cut -f1)
  test "$remote" = "$seed"
  printf 'API_PUSH_P_ONLY source_oid=%s external_oid=%s\n' "$oid" "$remote"
  rpc origin.publication.preview "$(jq -nc --arg uuid "$uuid" --arg oid "$oid" '{v:1,project:"notes-final-plumbing",expected_origin_url:"pdev@notes-review-origin:full.git",kind:"session",source:$uuid,source_oid:$oid,destination_ref:"refs/heads/main"}')" | tee /tmp/notes-final-plumbing-publication-preview.json
  ;;
 publish)
  oid=$(cat /tmp/notes-final-plumbing-oid)
  rpc origin.publish "$(jq -nc --arg uuid "$uuid" --arg oid "$oid" '{v:1,key:"notes-final-plumbing-publication",project:"notes-final-plumbing",expected_origin_url:"pdev@notes-review-origin:full.git",kind:"session",source:$uuid,source_oid:$oid,destination_ref:"refs/heads/main"}')"
  bash /etc/p-notes-origin-fixture.sh verify notes-final-plumbing refs/heads/main "$oid"
  ;;
 loss)
  rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')"
  result=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"notes-final-plumbing-loss",uuid:$uuid}')")
  wait_op "$(jq -r .result.operation.id <<<"$result")" | tee /tmp/notes-final-plumbing-loss.json
  loss=$(jq -r .result.operation.id <<<"$result")
  rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg loss "$loss" '{v:1,uuid:$uuid,kind:"delete",loss_operation_id:$loss}')" | tee /tmp/notes-final-plumbing-removal-preview.json
  ;;
 delete)
  token=$(jq -r .result.preview.confirmation_token /tmp/notes-final-plumbing-removal-preview.json)
  result=$(rpc session.delete "$(jq -nc --arg uuid "$uuid" --arg token "$token" '{v:1,key:"notes-final-plumbing-delete",uuid:$uuid,confirmation_token:$token}')")
  wait_op "$(jq -r .result.operation.id <<<"$result")"
  inc list "^p-$uuid$" --format json
  rpc project.delete.preview '{"v":1,"project":"notes-final-plumbing","loss_operations":{},"acknowledge_missing":[]}' | tee /tmp/notes-final-plumbing-project-preview.json
  ;;
 project-delete)
  token=$(jq -r .result.preview.confirmation_token /tmp/notes-final-plumbing-project-preview.json)
  result=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"notes-final-plumbing-project-delete",project:"notes-final-plumbing",confirmation_token:$token}')")
  wait_op "$(jq -r .result.operation.id <<<"$result")"
  ;;
esac

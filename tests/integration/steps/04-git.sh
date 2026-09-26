# shellcheck shell=bash
# Runs inside the disposable product-test VM as the confined pdev account.
# git-fixture seeds synthetic SQLite Git authority state. These operations do
# not claim public project/session lifecycle completion or a session runtime.
umask 077
set -E
step_dir="$P_TEST_TMP/step-04"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"

for name in server host blank work unknown; do
  ssh-keygen -q -t ed25519 -N '' -f "$step_dir/$name" >/dev/null
done

make_activation() {
  package="$1"
  target="$2"
  package_digest=$(p plugins conformance "$package" | jq -er '.sha256')
  package_id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$package_digest" --arg id "$package_id" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$target"
  p plugins activate "$target" | jq -e '.[0].capability == "source-git"' >/dev/null
}
make_activation "$P_TEST_GIT_PLUGIN" "$step_dir/bundled.json"
make_activation "$P_TEST_GIT_ALTERNATE" "$step_dir/alternate.json"

server_pid=
step04_error() {
  status="$1"
  line="$2"
  printf 'P_GIT_STEP04_FAIL line=%s status=%s\n' "$line" "$status" >&2
  if test -f "$step_dir/server.err"; then
    printf '%s\n' 'Git fixture server diagnostic (last 2 KiB):' >&2
    tail -c 2048 "$step_dir/server.err" >&2 || true
  fi
  if test -f "$step_dir/expected.err"; then
    printf '%s\n' 'Last expected SSH/Git refusal (last 1 KiB):' >&2
    tail -c 1024 "$step_dir/expected.err" >&2 || true
  fi
}
stop_server() {
  if test -n "${server_pid:-}"; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=
  fi
}
trap stop_server EXIT
trap 'step04_error "$?" "$LINENO"' ERR
start_server() {
  activation="$1"
  : > "$step_dir/server-ready.json"
  git-fixture serve "$state" "$activation" 127.0.0.1:0 "$step_dir/server" \
    > "$step_dir/server-ready.json" 2> "$step_dir/server.err" &
  server_pid=$!
  for ((attempt=0; attempt<100; attempt++)); do
    if jq -e '.schema == "p.git-fixture/v1" and .listen and .control' "$step_dir/server-ready.json" >/dev/null 2>&1; then
      break
    fi
    if ! kill -0 "$server_pid" 2>/dev/null; then
      cat "$step_dir/server.err" >&2
      echo 'Git fixture server exited before ready' >&2
      exit 1
    fi
    sleep 0.1
  done
  address=$(jq -er '.listen' "$step_dir/server-ready.json")
  socket=$(jq -er '.control' "$step_dir/server-ready.json")
  port=${address##*:}
  url="ssh://git@127.0.0.1:$port/app"
  printf '[127.0.0.1]:%s %s\n' "$port" "$(cat "$step_dir/server.pub")" > "$step_dir/known_hosts"
  fixture '{"schema":"p.git-fixture/v1","kind":"ping"}' | jq -e '.result.status == "ready"' >/dev/null
}
fixture() { git-fixture call "$socket" "$1"; }
expect_failure() {
  if "$@" > "$step_dir/unexpected.out" 2> "$step_dir/expected.err"; then
    echo "unexpected success: $*" >&2
    exit 1
  fi
}
set_git_key() {
  export GIT_SSH_COMMAND="ssh -i $1 -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$step_dir/known_hosts -o BatchMode=yes -o ConnectTimeout=5"
}
git_net() { timeout 30 git "$@"; }

start_server "$step_dir/bundled.json"
project_result=$(fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"app"}')
repo=$(jq -er '.result.repository_path' <<< "$project_result")
fixture "$(jq -nc --arg key "$step_dir/host.pub" '{schema:"p.git-fixture/v1",kind:"host-key",public_key:$key}')" | jq -e '.ok' >/dev/null
blank_result=$(fixture "$(jq -nc --arg key "$step_dir/blank.pub" '{schema:"p.git-fixture/v1",kind:"seed-session",key:"blank-main",project:"app",branch:"main",choice:"blank",public_key:$key}')")
blank_session=$(jq -er '.result.session_uuid' <<< "$blank_result")

set_git_key "$step_dir/host"
git_net clone "$url" "$step_dir/host-clone" >/dev/null 2>&1
set_git_key "$step_dir/blank"
git_net clone "$url" "$step_dir/blank-clone" >/dev/null 2>&1
git -C "$step_dir/blank-clone" config user.name P
git -C "$step_dir/blank-clone" config user.email p@example.invalid
printf 'first\n' > "$step_dir/blank-clone/README"
git -C "$step_dir/blank-clone" add README
git -C "$step_dir/blank-clone" commit -qm first
# A dry run and advertisement must leave the durable one-use grant available.
git_net -C "$step_dir/blank-clone" push --dry-run origin HEAD:main >/dev/null
git_net -C "$step_dir/blank-clone" push origin HEAD:main >/dev/null
git_net -C "$step_dir/blank-clone" fetch origin main >/dev/null
git-fixture probe "$address" "$step_dir/blank" | grep -Fx P_GIT_SSH_REQUESTS_REFUSED >/dev/null
# A consumed bootstrap grant cannot recreate a missing main after a fault.
main_oid=$(git -C "$repo" rev-parse refs/heads/main)
git -C "$repo" update-ref -d refs/heads/main
expect_failure git_net -C "$step_dir/blank-clone" push origin HEAD:main
if git -C "$repo" show-ref --verify --quiet refs/heads/main; then
  echo 'consumed blank grant recreated main' >&2
  exit 1
fi
git -C "$repo" update-ref refs/heads/main "$main_oid"

set_git_key "$step_dir/host"
git_net -C "$step_dir/host-clone" fetch origin main >/dev/null
git -C "$step_dir/host-clone" checkout -qB main origin/main
git -C "$step_dir/host-clone" config user.name P
git -C "$step_dir/host-clone" config user.email p@example.invalid
printf 'host write\n' > "$step_dir/host-clone/host.txt"
git -C "$step_dir/host-clone" add host.txt
git -C "$step_dir/host-clone" commit -qm host-write
expect_failure git_net -C "$step_dir/host-clone" push origin HEAD:main
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$main_oid"

git -C "$repo" update-ref refs/heads/work "$main_oid"
work_result=$(fixture "$(jq -nc --arg key "$step_dir/work.pub" '{schema:"p.git-fixture/v1",kind:"seed-session",key:"work-session",project:"app",branch:"work",choice:"existing",public_key:$key}')")
work_op=$(jq -er '.result.operation_id' <<< "$work_result")
fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"other"}' | jq -e '.ok' >/dev/null
other_url="ssh://git@127.0.0.1:$port/other"
set_git_key "$step_dir/host"
git_net ls-remote "$other_url" >/dev/null
set_git_key "$step_dir/work"
expect_failure git_net ls-remote "$other_url"
git_net clone "$url" "$step_dir/work-clone" >/dev/null 2>&1
git -C "$step_dir/work-clone" config user.name P
git -C "$step_dir/work-clone" config user.email p@example.invalid
git -C "$step_dir/work-clone" checkout -qb work origin/work
printf 'work one\n' > "$step_dir/work-clone/work.txt"
git -C "$step_dir/work-clone" add work.txt
git -C "$step_dir/work-clone" commit -qm work-one
git_net -C "$step_dir/work-clone" push origin HEAD:work >/dev/null
work_oid=$(git -C "$step_dir/work-clone" rev-parse HEAD)
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:other
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:main
git -C "$step_dir/work-clone" tag v1
expect_failure git_net -C "$step_dir/work-clone" push origin v1
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:refs/p/private
expect_failure git_net -C "$step_dir/work-clone" push --force origin HEAD~1:work
expect_failure git_net -C "$step_dir/work-clone" push origin :work
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$main_oid"
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
for absent_ref in refs/heads/other refs/tags/v1 refs/p/private; do
  if git -C "$repo" show-ref --verify --quiet "$absent_ref"; then
    echo "unauthorized ref exists: $absent_ref" >&2
    exit 1
  fi
done

printf 'work two\n' >> "$step_dir/work-clone/work.txt"
git -C "$step_dir/work-clone" commit -qam work-two
fixture "$(jq -nc --arg op "$work_op" '{schema:"p.git-fixture/v1",kind:"guard",project:"app",branch:"work",operation_id:$op}')" | jq -e '.ok' >/dev/null
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:work
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
stop_server
start_server "$step_dir/bundled.json"
set_git_key "$step_dir/work"
git_net -C "$step_dir/work-clone" remote set-url origin "$url"
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:work
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
fixture "$(jq -nc --arg op "$work_op" '{schema:"p.git-fixture/v1",kind:"unguard",project:"app",branch:"work",operation_id:$op}')" | jq -e '.ok' >/dev/null
git_net -C "$step_dir/work-clone" push origin HEAD:work >/dev/null
work_oid=$(git -C "$step_dir/work-clone" rev-parse HEAD)
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"

for number in $(seq -w 1 10); do
  git -C "$repo" update-ref "refs/heads/b$number" "$main_oid"
done
bundled_page=$(fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"app","limit":8}')
test "$(jq '.result.refs|length' <<< "$bundled_page")" -eq 8
test "$(jq '.result.broker_calls' <<< "$bundled_page")" -eq 1

# The alternate independently authored WASI package changes the receive
# ceiling and ref-page batching. A small FF push succeeds under that package,
# then the same queued oversized commit is refused there and accepted again
# under the bundled package.
stop_server
start_server "$step_dir/alternate.json"
git_net -C "$step_dir/work-clone" remote set-url origin "$url"
set_git_key "$step_dir/work"
alternate_page=$(fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"app","limit":8}')
test "$(jq -c '.result.refs' <<< "$alternate_page")" = "$(jq -c '.result.refs' <<< "$bundled_page")"
test "$(jq '.result.broker_calls' <<< "$alternate_page")" -eq 8
printf 'small alternate\n' >> "$step_dir/work-clone/work.txt"
git -C "$step_dir/work-clone" commit -qam alternate-small
git_net -C "$step_dir/work-clone" push origin HEAD:work >/dev/null
work_oid=$(git -C "$step_dir/work-clone" rev-parse HEAD)
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
dd if=/dev/urandom of="$step_dir/work-clone/random.bin" bs=1024 count=80 status=none
git -C "$step_dir/work-clone" add random.bin
git -C "$step_dir/work-clone" commit -qm large
large_oid=$(git -C "$step_dir/work-clone" rev-parse HEAD)
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:work
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
stop_server
start_server "$step_dir/bundled.json"
git_net -C "$step_dir/work-clone" remote set-url origin "$url"
set_git_key "$step_dir/work"
git_net -C "$step_dir/work-clone" push origin HEAD:work >/dev/null
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$large_oid"

# A hidden ref points to a unique commit unreachable from advertised heads.
tree=$(git -C "$repo" rev-parse "refs/heads/main^{tree}")
hidden_oid=$(printf 'hidden\n' | env GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid git -C "$repo" commit-tree "$tree" -p "$main_oid")
git -C "$repo" update-ref refs/p/secret "$hidden_oid"
set_git_key "$step_dir/host"
if git_net ls-remote "$url" | grep -F 'refs/p/secret'; then
  echo 'hidden ref was advertised' >&2
  exit 1
fi
expect_failure git_net -C "$step_dir/host-clone" fetch "$url" "$hidden_oid"
git -C "$repo" update-ref -d refs/p/secret
git -C "$repo" update-ref refs/heads-private/secret "$hidden_oid"
if git_net ls-remote "$url" | grep -F 'refs/heads-private/secret'; then
  echo 'hidden prefix sibling was advertised' >&2
  exit 1
fi
expect_failure git_net -C "$step_dir/host-clone" fetch "$url" "$hidden_oid"

page1=$(fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"app","limit":8}')
test "$(jq '.result.refs|length' <<< "$page1")" -eq 8
cursor=$(jq -er '.result.next' <<< "$page1")
page2=$(fixture "$(jq -nc --arg after "$cursor" '{schema:"p.git-fixture/v1",kind:"refs",project:"app",after:$after,limit:8}')")
test "$(jq '.result.refs|length' <<< "$page2")" -eq 4
test "$(jq -r '.result.next' <<< "$page2")" = ''

# An ordinary assigned branch cannot be recreated after it disappears.
git -C "$repo" update-ref -d refs/heads/work
set_git_key "$step_dir/work"
expect_failure git_net -C "$step_dir/work-clone" push origin HEAD:work
if git -C "$repo" show-ref --verify --quiet refs/heads/work; then
  echo 'ordinary session recreated missing work branch' >&2
  exit 1
fi

fixture "$(jq -nc --arg key "$step_dir/work.pub" '{schema:"p.git-fixture/v1",kind:"revoke",public_key:$key}')" | jq -e '.ok' >/dev/null
set_git_key "$step_dir/work"
expect_failure git_net ls-remote "$url"
fixture "$(jq -nc --arg session "$blank_session" '{schema:"p.git-fixture/v1",kind:"removing",session_uuid:$session}')" | jq -e '.ok' >/dev/null
set_git_key "$step_dir/blank"
expect_failure git_net ls-remote "$url"
set_git_key "$step_dir/host"
git_net ls-remote "$url" refs/heads/main | grep -F 'refs/heads/main' >/dev/null
set_git_key "$step_dir/unknown"
expect_failure git_net ls-remote "$url"

echo P_GIT_SSH_SUBSTRATE_PASS

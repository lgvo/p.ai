#!/usr/bin/env bash
# Runs as uid 1000 inside the disposable, NIC-less Incus guest.
set -euo pipefail
seed=/var/tmp/p-nix-seed
source_dir=/var/tmp/p-nix-source
cd /workspace
export HOME=/home/p NIX_PATH='' NIX_USER_CONF_FILES=/dev/null
nix_cmd() {
  timeout 60 nix --extra-experimental-features 'nix-command flakes' \
    --option accept-flake-config false --option pure-eval true \
    --option flake-registry '' \
    --option substituters '' "$@"
}
locked_flags=(--offline --no-update-lock-file --no-write-lock-file)
case "${1-}" in
  setup)
    test ! -e "$source_dir"
    mkdir -p "$source_dir/stdenv"
    fixture_bash=$(readlink -f /run/current-system/sw/bin/bash)
    fixture_bash_store=${fixture_bash%/bin/bash}
    test "$fixture_bash" != "$fixture_bash_store"
    test -d "$fixture_bash_store"
    sed "s#@BASH_STORE@#$fixture_bash_store#g" \
      "$seed/flake.nix" > "$source_dir/flake.nix"
    cp "$seed/setup" "$source_dir/stdenv/setup"
    git -C "$source_dir" init -q
    git -C "$source_dir" add flake.nix stdenv/setup
    git -C "$source_dir" -c user.name=P -c user.email=p@example.invalid commit -qm fixture
    git -C "$source_dir" rev-parse HEAD > /workspace/fixture-commit
    chmod -R a-w "$source_dir"
    test "$(nix --version)" = 'nix (Nix) 2.34.8'
    ;;
  capture)
    variant="${2:?variant}"
    commit=$(cat /workspace/fixture-commit)
    flake="git+file://$source_dir?rev=$commit#devShells.x86_64-linux.$variant"
    rm -f /tmp/p-nix-hook-marker
    nix_cmd build "${locked_flags[@]}" --no-link "$flake" \
      > "/workspace/build-$variant.out" 2> "/workspace/build-$variant.err"
    nix_cmd print-dev-env --json "${locked_flags[@]}" "$flake" \
      > "/workspace/capture-$variant.json" 2> "/workspace/capture-$variant.err"
    test "$(wc -c < "/workspace/build-$variant.err")" -lt 65536
    test "$(wc -c < "/workspace/capture-$variant.err")" -lt 65536
    test ! -e /tmp/p-nix-hook-marker
    test ! -e "$source_dir/flake.lock"
    ;;
  compare)
    variant="${2:?variant}"
    commit=$(cat /workspace/fixture-commit)
    flake="git+file://$source_dir?rev=$commit#devShells.x86_64-linux.$variant"
    rm -f /tmp/p-nix-hook-marker
    rm -f /tmp/p-nix-leak-marker
    export shellHook='printf leaked > /tmp/p-nix-leak-marker'
    nix_cmd develop "${locked_flags[@]}" "$flake" \
      --command bash "$seed/probe.sh" > "/workspace/develop-$variant.txt" \
      2> "/workspace/develop-$variant.err"
    test ! -e /tmp/p-nix-leak-marker
    rm -f /tmp/p-nix-hook-marker
    bash -c '. /etc/p/devshell/activation.sh; . /var/tmp/p-nix-seed/adapter-detail.sh "$1"; bash /var/tmp/p-nix-seed/probe.sh' \
      p-test "$variant" \
      > "/workspace/adapter-$variant.txt" 2> "/workspace/adapter-$variant.err"
    test ! -e /tmp/p-nix-leak-marker
    cmp "/workspace/develop-$variant.txt" "/workspace/adapter-$variant.txt"
    test "$(wc -c < "/workspace/develop-$variant.err")" -lt 65536
    test "$(wc -c < "/workspace/adapter-$variant.err")" -lt 65536
    test ! -e "$source_dir/flake.lock"
    ;;
  negative)
    fixture_commit=$(cat /workspace/fixture-commit)
    if nix_cmd build "${locked_flags[@]}" --no-link \
      "git+file://$source_dir?rev=$fixture_commit#devShells.x86_64-linux.failing" \
      > /workspace/failing-build.out 2> /workspace/failing-build.err; then
      echo 'failing derivation built successfully' >&2; exit 1
    fi
    test ! -s /workspace/failing-build.out
    test -s /workspace/failing-build.err
    test "$(wc -c < /workspace/failing-build.err)" -lt 65536
    grep -Eiq 'fail|exit code 17' /workspace/failing-build.err

    invalid=/var/tmp/p-nix-invalid
    mkdir -p "$invalid"
    cat > "$invalid/flake.nix" <<'NIX'
{
  outputs = { self }: {
    devShells.x86_64-linux.default = 42;
  };
}
NIX
    git -C "$invalid" init -q
    git -C "$invalid" add flake.nix
    git -C "$invalid" -c user.name=P -c user.email=p@example.invalid commit -qm invalid
    invalid_commit=$(git -C "$invalid" rev-parse HEAD)
    if nix_cmd print-dev-env --json "${locked_flags[@]}" \
      "git+file://$invalid?rev=$invalid_commit#devShells.x86_64-linux.default" \
      > /workspace/invalid.out 2> /workspace/invalid.err; then
      echo 'invalid devShell was accepted' >&2; exit 1
    fi
    test ! -s /workspace/invalid.out
    test -s /workspace/invalid.err
    test "$(wc -c < /workspace/invalid.err)" -lt 65536

    locked=/var/tmp/p-nix-lock-required
    mkdir -p "$locked/dep"
    cat > "$locked/dep/flake.nix" <<'NIX'
{ outputs = { self }: { marker = "local input"; }; }
NIX
    cat > "$locked/flake.nix" <<'NIX'
{
  inputs.dep.url = "path:./dep";
  outputs = { self, dep }: {
    devShells.x86_64-linux.default = dep.marker;
  };
}
NIX
    git -C "$locked" init -q
    git -C "$locked" add flake.nix dep/flake.nix
    git -C "$locked" -c user.name=P -c user.email=p@example.invalid commit -qm missing-lock
    locked_commit=$(git -C "$locked" rev-parse HEAD)
    if nix_cmd print-dev-env --json "${locked_flags[@]}" \
      "git+file://$locked?rev=$locked_commit#devShells.x86_64-linux.default" \
      > /workspace/lock.out 2> /workspace/lock.err; then
      echo 'missing lock was accepted' >&2; exit 1
    fi
    test ! -s /workspace/lock.out
    test -s /workspace/lock.err
    test "$(wc -c < /workspace/lock.err)" -lt 65536
    grep -Eiq 'lock|update' /workspace/lock.err
    test ! -e "$locked/flake.lock"
    ;;
  *) echo 'usage: guest.sh setup|capture VARIANT|compare VARIANT|negative' >&2; exit 2 ;;
esac

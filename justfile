set positional-arguments

# List the available development commands.
default:
    @just --list

# Build the packaged CLI, bundled plugins, and run its Nix build checks.
build:
    nix build .#default

# Run flake evaluation, unit tests, package checks, and the full VM suite.
test: check unit-tests build vm-tests

# Run Go, Python, and mocked VM runner tests against the working checkout.
unit-tests:
    #!/usr/bin/env bash
    set -euo pipefail
    # Leave room for Go's test names within Linux's Unix socket path limit.
    p_test_tmp=$(mktemp -d /tmp/p.XXXXXX)
    trap 'rm -rf -- "$p_test_tmp"' EXIT
    export TMPDIR="$p_test_tmp"
    go test ./...
    python3 -I -B -m unittest discover -s tests/unit -p '*_test.py'
    bash tests/integration/test-vm-selection.sh

# Evaluate the flake without building its outputs.
check:
    nix flake check --no-build

# Format production Go sources.
fmt:
    gofmt -w cmd internal pkg plugins runtime

# Open the persistent P lab; accepts --public or --help.
lab *args:
    ./dev/demo-vm "$@"

# Open the persistent lab with public DNS/HTTP(S) enabled.
lab-public:
    ./dev/demo-vm --public

# Run fresh VM integration checks; accepts --step STEP.sh selections.
vm-tests *args:
    ./dev/test-vm "$@"

# Run the Incus backend infrastructure checks in a disposable VM.
vm-incus-tests:
    nix run path:./dev/vm#smoke

[private]
alias test-vm := vm-tests

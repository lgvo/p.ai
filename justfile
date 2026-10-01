set positional-arguments

# List the available development commands.
default:
    @just --list

# Build the packaged CLI, bundled plugins, and run its Nix build checks.
build:
    nix build .#default

# Run flake evaluation, unit tests, package checks, and the full VM suite.
test: check unit-tests tui-tests build vm-tests

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

# Open the public lab with seeded P and three-service notes projects.
lab-public:
    ./dev/demo-vm --public

# Reset the stopped public lab after explicit confirmation; --offline selects the offline lab.
lab-reset *args:
    python3 ./dev/reset-lab-vm "$@"

# Compatibility alias for the consolidated public lab.
alias lab-notes := lab-public

# Test the notes sample against a temporary real PostgreSQL cluster.
notes-tests:
    nix-shell dev/notes-shell.nix --run 'python3 examples/notes/tests.py'

# Explore the production TUI against a disposable fixture socket and local tmux.
tui-mock *args:
    ./dev/tui-mock "$@"

# Check all production TUI actions, sockets, and terminal transport without a VM.
tui-tests:
    nix-shell dev/tui-test-shell.nix --run 'python3 -B tests/integration/tui-terminal-test.py && python3 -B tests/integration/tui-observe-test.py && python3 -B tests/integration/tui-mock-test.py'

# Run fresh VM integration checks; accepts --step STEP.sh selections.
vm-tests *args:
    ./dev/test-vm "$@"

# Run the Incus backend infrastructure checks in a disposable VM.
vm-incus-tests:
    nix run path:./dev/vm#smoke

[private]
alias test-vm := vm-tests

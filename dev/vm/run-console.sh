#!/usr/bin/env bash
set -euo pipefail

if (( $# == 0 )) || ! [[ -t 0 && -t 1 ]]; then
  echo 'Usage: run-console.sh COMMAND [ARG ...] (interactive terminal required)' >&2
  exit 2
fi

p_console_tty_state=$(stty -g)
restore_console() {
  stty "$p_console_tty_state"
}
trap restore_console EXIT
p_console_pid=''
stop_console() {
  local signal=$1 status=$2
  trap '' HUP INT TERM
  if [[ -n $p_console_pid ]]; then
    kill -s "$signal" "$p_console_pid" 2>/dev/null || true
    wait "$p_console_pid" 2>/dev/null || true
  fi
  exit "$status"
}
trap 'stop_console HUP 129' HUP
trap 'stop_console INT 130' INT
trap 'stop_console TERM 143' TERM

# QEMU's stdio backend enables OPOST using the modes saved at startup.
# Clear ONLCR before that snapshot so guest raw-mode LF cursor movements
# reach the host terminal unchanged even after QEMU enables OPOST.
stty -onlcr
# Keep the tty input for the asynchronous child; explicit wait lets signal
# handlers forward cancellation before restoring modes after the child exits.
"$@" <&0 &
p_console_pid=$!
wait "$p_console_pid"

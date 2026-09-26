# shellcheck shell=bash
# Accept the console's optional CR before LF, while matching the entire line.
vm_log_has_marker() {
  local log_file=$1 marker=$2
  grep -Fxq -e "$marker" -e "$marker"$'\r' "$log_file"
}

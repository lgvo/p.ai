#!/usr/bin/env sh
set -eu

bin=${1:-.cache/bin/p-tui-probe}
out=evidence/frames
mkdir -p "$out"

for variant in table navigator attention command focus lanes; do
  "$bin" --snapshot --variant "$variant" --width 80 --height 24 | sed 's/[[:space:]]*$//' >"$out/$variant-80x24.txt"
  "$bin" --snapshot --variant "$variant" --width 120 --height 35 | sed 's/[[:space:]]*$//' >"$out/$variant-120x35.txt"
done

for scenario in attached-switch create-failed replacement-create branches policy delete-preview delete-progress delete-complete help; do
  "$bin" --snapshot --scenario "$scenario" --width 80 --height 24 | sed 's/[[:space:]]*$//' >"$out/$scenario-80x24.txt"
  "$bin" --snapshot --scenario "$scenario" --width 120 --height 35 | sed 's/[[:space:]]*$//' >"$out/$scenario-120x35.txt"
done

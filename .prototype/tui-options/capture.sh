#!/usr/bin/env sh
set -eu

bin=${1:-.cache/bin/p-tui-probe}
out=evidence/frames
expanded=evidence/expanded
mkdir -p "$out"
mkdir -p "$expanded"

for variant in table navigator attention command focus lanes outline matrix operations workspace minimal cards attachment governance compare topology actions; do
  "$bin" --snapshot --variant "$variant" --width 80 --height 24 | sed 's/[[:space:]]*$//' >"$out/$variant-80x24.txt"
  "$bin" --snapshot --variant "$variant" --width 120 --height 35 | sed 's/[[:space:]]*$//' >"$out/$variant-120x35.txt"
done

for variant in table navigator attention command focus lanes outline matrix operations workspace minimal cards attachment governance compare topology actions; do
  for size in 160x50 132x40 120x35 100x30 80x24 60x20 48x16 40x12; do
    width=${size%x*}
    height=${size#*x}
    "$bin" --snapshot --dataset standard --variant "$variant" --width "$width" --height "$height" | sed 's/[[:space:]]*$//' >"$expanded/standard-$variant-$size.txt"
  done
done

for dataset in dense empty single long; do
  for size in 160x50 80x24 60x20 48x16; do
    width=${size%x*}
    height=${size#*x}
    "$bin" --snapshot --dataset "$dataset" --variant table --width "$width" --height "$height" | sed 's/[[:space:]]*$//' >"$expanded/$dataset-table-$size.txt"
  done
done

for scenario in attached-switch create-failed replacement-create branches policy delete-preview delete-progress delete-complete help; do
  "$bin" --snapshot --scenario "$scenario" --width 80 --height 24 | sed 's/[[:space:]]*$//' >"$out/$scenario-80x24.txt"
  "$bin" --snapshot --scenario "$scenario" --width 120 --height 35 | sed 's/[[:space:]]*$//' >"$out/$scenario-120x35.txt"
done

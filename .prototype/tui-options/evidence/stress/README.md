# Data-stress evidence

This directory is deliberately separate from the curated interface-comparison
frames in `evidence/expanded` and `evidence/frames`.

Each child directory is one generated stress family. Frames cover all 17
interfaces at 120×35, 80×24, 60×20, and 48×16. Regenerate them with:

```sh
sh capture-stress.sh
sh capture-churn.sh
```

The `baseline` family is the control for the stress harness. Later families
change one material data pressure at a time. `churn-sequence` adds five ordered
revisions for every interface and target viewport; the ordinary `churn` family
is its step-zero control.

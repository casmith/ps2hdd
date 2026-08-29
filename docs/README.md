# ps2hdd documentation

- [compatibility.md](compatibility.md) — every upstream fact ps2hdd depends on,
  with its source: the APA and HDLoader on-disk layouts, `hdl_dump` syntax and
  output formats, `pfsfuse` usage, OPL's artwork naming and sizes, the
  POPStarter layout, the POPS VCD format, and the artwork providers. Also
  records where the implementation deviates from the original plan, and why.
- [safety.md](safety.md) — what ps2hdd refuses to do, the checks that run
  before every write, and how to grant raw device access without running
  anything as root.
- [hardware-validation.md](hardware-validation.md) — the manual checklist that
  covers what the automated suite cannot: that a game installed by ps2hdd
  actually boots on a console.
- [PROJECT_PLAN.md](PROJECT_PLAN.md) — the original specification.

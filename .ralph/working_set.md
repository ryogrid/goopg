# Working Set — Loop #23 → #24

Task: **M0146-0009d** (ea-ratchet vacuous-PASS hardening). DONE — committed
this loop; see git log for sha. Design `docs/design/0100-0149/m0146-0009d-ea-ratchet-vacuous-pass.md`;
evidence `analysis/m0146/m0146-0009d/` + `tmp/m0146-0009d-gate-run.log`.

Files:
- `scripts/estimate-parity-gate.sh` — `verify_server` (pidfile-in-EA_DATA →
  live pid → cmdline `start -D EA_DATA` → pidfile ListenAddr covers EA_PORT
  → wire `version()` contains `goopg`), `EA_VERIFY_ONLY=1` seam, corpus
  sentinel (`store_sales`), hard ANALYZE gate (`reltuples>0`), post-capture
  zero-section/all-ERROR refusal (unanchored `ERROR:|FATAL:` — psql prints
  `psql:<file>:<line>: ERROR:` mid-line).
- `scripts/estimate-parity/parity.py` — `queries==0`/`scored==0` → exit 2
  BEFORE `--write-baseline`/ratchet (the 0-entry-baseline clobber path).
- `scripts/estimate-parity-gate-test.py` — 14 unittest cases, all PASS.

Findings:
- Verified live: foreign postgres (pid 1748354, `tmp/pg-probe-data`) still
  squats on :5534 — gate now refuses with exit 2 instantly, both in
  EA_VERIFY_ONLY and full-run mode. Do NOT kill it (not ours).
- Valid run `EA_PORT=5541` on torn-WAL clone (GOOPG_WAL_ALLOW_EARLY_END=1):
  227 nodes scored, FINDINGS 16 = baseline 16 → EA-RATCHET: PASS.
- Q36/Q70/Q86 carry a pre-existing trailing-`;` syntax ERROR inside
  otherwise-scored sections — partial errors are allowed by design.

Gates run: gate-test 14/14 PASS; live foreign-server refusal PASS; full
ea-ratchet on :5541 PASS (16/16). Scripts-only change — no Go gates needed;
pgbench smoke runs in the pre-commit hook.

Next step: per banner — **M0146-0010** (`Materialize` node, four slices per
`docs/design/0100-0149/m0144-0011c-materialize-sizing.md` §4) is the next
M0146 item in file order; the three M-NIGHTLY isolation regressions and the
explicit-opclass clean-restart task remain parked under items 2a/10.

In-flight: none.

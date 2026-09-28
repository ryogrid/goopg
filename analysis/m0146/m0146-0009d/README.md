# M0146-0009d — ea-ratchet vacuous-PASS hardening: evidence

Design doc: `docs/design/0100-0149/m0146-0009d-ea-ratchet-vacuous-pass.md`.

## The incident being fixed (observed twice, 2026-09-28)

A foreign PostgreSQL postmaster (pid 1748354, `-D tmp/pg-probe-data`)
holds the gate's default `EA_PORT=5534`. Under the old code:

- `pg_isready` succeeded → `start_server` returned 0 without starting
  anything;
- all 99 psql captures returned `ERROR: relation "…" does not exist`,
  but `ON_ERROR_STOP=0` keeps the client exit code 0;
- `parity.py` scored `nodes scored: 0`, produced 0 findings, and the
  ratchet printed `PASS (52 fixed)`;
- `ea-ratchet-repin` then wrote a **0-entry baseline** over the pinned
  52-entry one (reverted via `git checkout`; the valid repin on port
  5541 wrote the correct 16-entry baseline).

## Live verification of the fix

While the foreign postgres was still holding :5534:

```
$ EA_VERIFY_ONLY=1 EA_PORT=5534 bash scripts/estimate-parity-gate.sh
[ea] verify FAILED: :5534 is not this gate's goopg on …/tmp/c20a/data-sf025
EXIT=2

$ EA_PORT=5534 bash scripts/estimate-parity-gate.sh
[ea] building …/tmp/c20a/goopg-ea
[ea] starting goopg on 127.0.0.1:5534 (data …/tmp/c20a/data-sf025, cgroup goopg-ea-ratchet)
[ea] port 5534 is held by a server that is not this gate's goopg on
     …/tmp/c20a/data-sf025 — refusing to capture against it
(exit 2, immediate — previously this ran the full 99-query capture)
```

Full gate log: `tmp/m0146-0009d-gate-run.log` (gitignored tmp/).

## Positive path

`GOOPG_WAL_ALLOW_EARLY_END=1 EA_PORT=5541 bash scripts/estimate-parity-gate.sh`
against the private clone (`tmp/c20a/data-sf025`, torn WAL tail — the
sanctioned throwaway-clone override): server identity verified, corpus
check passed, ANALYZE gate passed (48 relations), 99-query capture
(7,612 lines, no psql failures, 3 non-fatal ERROR lines inside two
sections — below the all-ERROR bar), 227 nodes scored, and

```
queries:   99   nodes scored: 227   unmatched-in-PG: 18
FINDINGS:  16
RATCHET: baseline findings: 16   current: 16
EA-RATCHET: PASS
```

— identical to the repinned baseline, so the hardened gate produces the
same verdict the unhardened one did on a healthy capture.

## Unit coverage

`scripts/estimate-parity-gate-test.py` — 14 cases, all PASS:

- parity.py: all-ERROR / empty / section-less captures → exit 2;
  vacuous capture cannot write a baseline nor produce a ratchet PASS;
  valid 2-node capture scores and exits 0; partial-error corpus remains
  scorable.
- gate `EA_VERIFY_ONLY`: no pidfile / dead pid / live non-goopg pid
  (recycled-pidfile shape) / malformed pidfile → exit 2.
- gate `EA_CAPTURE` end-to-end: all-error file → exit 2 through the real
  script; valid file → reports and exits 0.

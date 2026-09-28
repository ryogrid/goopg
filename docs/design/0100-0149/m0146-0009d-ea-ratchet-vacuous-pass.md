# M0146-0009d — ea-ratchet vacuous-PASS hardening

Parent: M0146-0009 (EA ratchet) / surfaced live during M0146-0009b and again
during M0146-0009e's repin. Kind: impl (harness).

## Problem

`scripts/estimate-parity-gate.sh` could print a PASS that measured nothing.
Observed 2026-09-28 (twice): a foreign PostgreSQL process held the default
`EA_PORT=5534` (a leftover probe cluster on `tmp/pg-probe-data`,
pid 1748354), so:

1. `pg_isready` in `start_server` short-circuited — it proves only that
   *something* answers the wire protocol, not that it is this gate's goopg.
2. The capture phase ran all 99 queries against the foreign server; every
   statement returned `ERROR: relation "…" does not exist`. Because the
   psql invocation uses `-v ON_ERROR_STOP=0`, the client exit code stays 0
   and the per-query `FAILED` check never fires.
3. `parity.py` scored 0 nodes, produced 0 findings, and the ratchet
   printed `PASS (52 fixed)`.
4. `ea-ratchet-repin` then wrote a **0-entry baseline** over the pinned
   52-entry one (reverted; a valid repin on `EA_PORT=5541` later wrote the
   correct 16-entry baseline).

A second shape of the same defect: an `EA_DATA` clone that lacks the TPC-DS
tables (stale or wrongly seeded) produces the identical all-ERROR capture —
the server genuinely is ours, so identity alone cannot catch it.

## Fix

Three independent refusal points, each fail-closed:

### 1. Server identity (`verify_server` in the gate)

After `pg_isready` succeeds — whether on the pre-check short-circuit or
after spawning our own server — the gate now requires ALL of:

- `${EA_DATA}/postmaster.pid` exists and names a **live** pid
  (`/proc/<pid>/cmdline` readable). A pidfile inside the data dir can only
  be written by a server started on that dir.
- That pid's cmdline carries `start` and `-D <EA_DATA>` (as-passed or
  canonicalised via `realpath`). A stale pidfile naming a recycled pid —
  or the foreign `postgres -D tmp/pg-probe-data` postmaster — fails here.
- The pidfile's recorded `ListenAddr` line ends in `:${EA_PORT}` (goopg's
  pidfile stores `PID/DataDir/StartedAt/ListenAddr/SocketPath`, mirroring
  upstream's `postmaster.pid`).
- Wire-level engine check: `SELECT version()` contains `goopg` — goopg
  reports `PostgreSQL 18.3 goopg compatible`; a real PostgreSQL never
  emits that string.

A responder already on the port is reused only when all four hold —
legitimate reuse (a leftover server from an earlier gate run on the same
clone) still works; anything else is refused with exit 2 instead of being
scored. When our own spawn dies but the port still answers, the wait loop
fails fast rather than polling out `EA_READY_TRIES`.

`EA_VERIFY_ONLY=1` exposes `verify_server` standalone (test seam and
debugging aid) without building or starting anything.

### 2. Corpus + ANALYZE sanity

After the verified server is up:

- `store_sales` must resolve in `pg_class` — catches a stale `EA_DATA`
  clone (the shape where identity is correct but the corpus is absent).
- `ANALYZE` must leave `reltuples > 0` on at least one `pg_class` row —
  a blind-planner capture measures default selectivities, not estimators.

### 3. Vacuous-capture refusal

- Gate level: after the capture loop, count `===== Qn =====` sections vs
  sections containing an `ERROR:` line. Zero sections, or every section
  errored → exit 2 before scoring (partial errors still score — a corpus
  with some broken queries is a measurement, all-broken is not).
- Scorer level (`parity.py`): `queries == 0` or `scored == 0` prints
  `EA PARITY: FAIL — … refusing to score a vacuous capture` and exits 2 —
  **before** `--write-baseline` writes anything and before the ratchet
  compares baselines. This protects every consumer: the live gate,
  `EA_CAPTURE` re-scores, and repins. Exit 2 distinguishes infrastructure
  failure from the ratchet's exit-1 findings FAIL.

## Why these layers together

`pg_isready` answers "does something listen", `verify_server` answers
"is it ours, on our datadir", the corpus check answers "does our datadir
contain the corpus", the capture check answers "did queries produce
plans", and `scored == 0` answers "did plans parse to nodes". Each layer
is redundant against the failure modes of the layers above it — the
all-ERROR path in particular crosses two of them (foreign responder →
identity; stale clone → corpus; capture shape → both post-checks).

## Files

- `scripts/estimate-parity-gate.sh` — `verify_server`, `EA_VERIFY_ONLY`,
  corpus/ANALYZE/capture gates, identity-checked `start_server`.
- `scripts/estimate-parity/parity.py` — vacuous-capture refusal.
- `scripts/estimate-parity-gate-test.py` — 14 unittest cases: all-error /
  empty / section-less captures → exit 2; vacuous capture cannot write a
  baseline or ratchet-PASS; verify_server negatives (no pidfile, dead pid,
  live non-goopg pid, malformed pidfile); EA_CAPTURE end-to-end wiring.

## Evidence

- `tmp/m0146-0009d-gate-run.log` — live refusal against the real foreign
  postgres on :5534 (exit 2, immediate) and a full valid run on :5541.
- `analysis/m0146/m0146-0009d/README.md`.

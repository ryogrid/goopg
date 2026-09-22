# M0145-0021b — the TPC-H fire-set lane, and the unpinned capture it exposed

Status: **LANDED 2026-09-22.** Movement: none — harness. The corpus-extension
this task names is the smaller half of what it found.
Kind: impl
Parent: M0145-0021

## What the task asked for

M0145-0021 landed a two-scale fire-set gate over the TPC-DS corpora and
ledgered gap (b): `scripts/jointree-parity-capture.sh`'s `tpch` branch returns
BEFORE the `FIRESET_QUERIES` execution block, so the template covered TPC-DS
only — while the TPC-H floor pin is where M0144-0001 measures parity.

## What it found first: the TPC-H capture lane was not reproducible

Before extending a gate that DERIVES its fire set from a plan A/B, the
derivation has to have a zero noise floor. It did not. Two back-to-back
captures, same binary, same arm (`JOINTREE=0`), nothing changed in between:

```
=== PLAN-SHAPE: queries=21 same=1 changed=20 added=0 removed=0 ===
```

20 of 21 queries differed. Separating cost churn from shape churn:

- 20 queries differed in **cost/rows only** (e.g. `Parallel Seq Scan on
  lineitem … rows=1480262` vs `rows=1477032`);
- **Q3 flipped SHAPE**: `GroupAggregate` over `Gather Merge` over `Sort`
  became `HashAggregate` over `Gather` over `Hash Join`.

The parity numbers moved with it: the same two runs reported
`aggregation-strategy=6 sort-strategy=10` and `…=5 …=9`, and
`D4-upperrel 5 / jointree-search 4` against `4 / 5`.

**Cause.** `scripts/tpch-acceptance-arm.sh` pins `GOOPG_ANALYZE_SEED`
(default `20260905`) and its header says why: goopg's statistics are
per-connection and ANALYZE is sampled, so an unpinned arm plans against a
different sample than its counterpart — measured A/A noise of 455 estimate
lines and 27 plan-shape lines, "LARGER than the A/B signal most planner changes
carry". `scripts/tpch-estimate-audit-arm.sh`, which is the lane the **plan
parity** capture uses, exported `GOOPG_PGSHAPED_DP`, `GOOPG_JOINTREE_PIPELINE`,
`GOMEMLIMIT` and the memory caps — and never the seed. The TPC-DS lanes get
theirs from `bench/tpcds/env_tpcds.sh`; only TPC-H was unpinned.

**Fix and verification.** One export, same default and same reason. The A/A
re-run after it:

```
=== PLAN-SHAPE: queries=22 same=22 changed=0 added=0 removed=0 ===
```

This matters well beyond this task: it is the canonical TPC-H plan-parity arm
(M0144-0001), so every `CATEGORIES-EXCL-MATCH` comparison taken on it before
today carried this noise. A ±1 category delta on that lane was not necessarily
a signal. Captures taken before and after this commit are not comparable;
re-take the baseline rather than diffing across the pin.

## Second defect: a sub-labelled block was credited to its neighbour

`queries=21` above is not 22 by accident. The TPC-H capture writes
`=== Q15a-VIEWBODY` and no plain `=== Q15` block, and
`scripts/tpcds-plan-diff.py`'s `BLOCK_RE` stopped at the digits, so that header
matched nothing: `cur` stayed on the preceding query and Q15a's plan was
appended to **Q14's** block. Q15 was absent from every comparison and Q14's was
against a body that is not Q14's.

The regex now accepts a sub-label and folds the block onto its numeric id —
the id space the fire-set gate needs — separated by a `-- block Q15a-VIEWBODY`
marker so a renamed or reordered sub-block is still a difference. TPC-DS
captures have no sub-labels, so nothing changes there; the count went
`queries=21` -> `queries=22`.

## The extension itself

TPC-H has no query `.sql` files in the tree — its bank lives in
`cmd/tpch-runner` — so the TPC-DS recipe (`psql -f query<n>.sql`, read the exit
status) does not port. The fire set is executed by `tpch-acceptance-arm.sh`
with `QUERIES=<ids>` on its own private clone, and
`scripts/tpch-fireset-parse.py` maps the arm's per-query lines onto the same
three-status vocabulary the comparator reads:

| arm line | status |
|---|---|
| `Q7: OK elapsed=1.23s …` | `PASS` |
| `Q9: ERROR after 600.10s — … user request (57014)` | `TIMEOUT` |
| `Q9: ERROR after 0.02s — … syntax error …` | `ERROR` |

That mapping is the load-bearing part: the runner enforces the per-query budget
as a statement timeout, so an over-budget query returns as an ERROR carrying
SQLSTATE 57014. Classifying it as `ERROR` would hide exactly the class the gate
exists to catch; classifying every ERROR as `TIMEOUT` would invent them. One
numeric id can produce several labels (Q15 runs as `Q15-CREATEVIEW`,
`Q15a-VIEWBODY`, `Q15b-MAIN`); they fold worst-outcome-first. An id with no
line at all is fatal, because a silently missing record reads as "not executed
yet" to the gate's resume logic.

Two defaults are deliberate:

- **`PGSHAPED=1`**, not the arm script's own `0`. That default is not the
  planner goopg ships, and measuring a non-shipped planner is how TPC-H Q9
  produced a false red gate for three loops
  (`m0145-0020a-grouped-output-cardinality.md`).
- **`GATE_STAMP_DIR` redirected** into the run's own output directory. A
  `QUERIES` subset always stamps `NO-COMPARE`, and a fire-set execution must
  never destroy — or fabricate — the real acceptance-arm verdict.

`tpch` is a supported `CORPORA` value but **not** a default: its fires execute
at SF1, so it is opt-in
(`CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh …`).

## Evidence

- A/A before the pin: `same=1 changed=20`, Q3 shape flip. After: `same=22
  changed=0`. Captures under `tmp/m0145-0021b-aa/`.
- Execution half, real run: `FIRESET_QUERIES=12 FIRESET_SKIP_CAPTURE=1` wrote
  `Q12 PASS` from a `Q12: OK elapsed=4.33s … rows=2` arm line, at
  `GOOPG_PGSHAPED_DP=1`, with `tmp/gate-stamps/tpch-acceptance-arm.json`
  verified byte-identical across the run.
- TIMEOUT classification verified against the REAL runner, not a fixture:
  the same query at `FIRESET_TIMEOUT=1` produced `Q12: ERROR after 1.00s — pq:
  canceling statement due to user request (57014)` and the record `Q12
  TIMEOUT`.
- `tpch-fireset-parse.py --self-test` 5/5; `tpcds-plan-diff-test.py` 12 cases
  (2 new). Non-vacuity checked: neutralising the sub-label match fails exactly
  those two and leaves the other ten green.

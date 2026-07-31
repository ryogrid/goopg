Task: **M0125-0034's join-order arm** — LANDED and committed. The item stays
UNCHECKED for Q65 only (derived tables; needs a parser change first).

**Next loop: read the `## Current Priority` banner FIRST.** It now names
**`M0125-0044`** as the next selection, on this milestone's own "a silent wrong
answer outranks a timeout on severity" rule. Then `-0034`'s Q65 remainder /
`-0035`'s CTE-body arm, then `M0125-0038` last.

Files: `internal/planner/joinorder.go` (connectivity mode);
`internal/planner/joinorder_connectivity_test.go` (8 tests, new);
`docs/design/0125-0034a-comma-from-connectivity-order.md` + README row;
`analysis/m0125-0034b/` (gate + probes).
Key symbols: `reorderCommaFromByCardinality`, `orderByConnectivity`,
`orderByCardinality`, `firstUnused`.

Findings — do NOT re-derive:
- **Both** join-order passes declined on any comma-FROM list holding a WITH
  reference, neither of them deciding anything: `tryBushyDP` on its leaf
  whitelist (`*SeqScan`/`*IndexScan`/`*MultiHashJoin`; `buildBindingsPosMap`
  keys on scan identity) plus `len(tables) > 12`, and the comma-FROM greedy on
  "not a base table with `Stats.RowCount`". Same shape as -0041.
- Fix is at the **parser level** (permutes before column resolution → no
  `ColumnRef.Index` remapping), which is why it costs none of the posmap risk.
  Connectivity mode is a **fixed point on any cross-free source order**, so it
  fires iff the source order has an avoidable cross.
- **On the S-cold SF0.5 cluster, cardinality mode never fires at all** (goopg
  drops `TableStats.RowCount` on restart), so every reorder measured is
  connectivity mode.
- Trap already paid for: `tables[]` feeds `buildBareColumnIndex`, which needs
  **columns**, not `Stats` — gating it on stats silently empties the
  bare-column map and kills every edge.
- **`M0125-0044` is NEW and is the next selection**: three `date_dim` aliases
  collapse to one in projection resolution (Q64 answers 0 vs oracle 2). Proven
  pre-existing by a byte-identical A/B (arm A fires the pass, arm B declines);
  reduced to 6 relations in `analysis/m0125-0034b/alias_a.sql`. Q64 does NOT
  complete at HEAD in 1848 s — that is why it was invisible.
- Q72's TIMEOUT → PASS (309 s) is NOT this change: Q72 has no `WITH`.
- TPC-H inert **by construction** — the TPC-H query set has no `WITH … AS (`.

Gates run this loop (all PASS): `go build ./...`; `go vet ./internal/planner/`;
`go test ./internal/planner/... ./internal/executor/`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`scripts/tpch-spotcheck.sh` (RESULT=PASS, Q12=2 Q13=35, 32.2 s);
**full 99-query TPC-DS SF0.5 gate, one binary, 3 chunks — PASS=92 MISMATCH=1
CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4; exactly 4/99 cells moved vs
`sweep-20260731-121447.txt`**; pre-commit pgbench smoke (hook);
`make ralph-state-guard`.

In-flight: none. All bench servers stopped (65433/65436/65437 down).
PG oracle :65438 was already UP from a prior loop and is left UP, untouched.

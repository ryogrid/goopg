# M0139-0007a (arm 2/2) — measure `GOOPG_PG_SORT_RELATION_BYTES_COST` (R113)

Status: accepted (measured 2026-09-15; **decision: HOLD — stays default-off**)

## Task

Second of the two arms M0139-0007a's filing asks for, measured independently
of the first per the recon's own warning ("row 3 feeds row 1's competing
Sort-based plan shapes, so a combined flip could move a plan for a reason
neither arm alone explains"). The companion arm
(`GOOPG_PG_HASH_TUPLE_SPILL_COST`, R108) is measured in
`m0139-0007a-hash-tuple-spill-cost-measurement.md`; that document's "What the
arm already is", "Verification" and "No production diff" sections apply here
unchanged in substance (same measurement task, same binary, no new
derivation) and are not repeated at length.

## What the arm already is (no new derivation — this is a measurement task)

`sort_pgrelationbytes.go` (R113, M0139-0007's recon inventory row 3) ports
PG's `relation_byte_size` (`MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader)`,
`postgres/src/backend/optimizer/path/costsize.c`) as `pgRelationByteSize`,
keyed on `pathWidth`/`Rel.Width` — a field `path.go:686-691`'s own comment
already documents as deliberately carrying PG's packed-tuple-width currency,
separate from the map-footprint model. `costSortRunWithWidth`
(`cost_funcs.go:296`) elects this currency for the Sort spill decision only
when `pgSortRelationBytesCostEnabled()` is true. This same costing function
also prices WindowAgg's internal sort via `costWindow`
(`windowsetoppaths.go`), so the arm's reach extends beyond bare `ORDER BY`/
`Sort` nodes into any window function that needs one. Nothing about the
mechanism changed in this task — it is measurement only.

## Method

Identical procedure and controls to the companion R108 document (single
binary, private ports, same-PG-reference / matching-stats-epoch controls,
`shape-delta.sh`), substituting `GOOPG_PG_SORT_RELATION_BYTES_COST=1`:

- **TPC-H**: private port 5593. Artefacts:
  `analysis/m0139/m0139-0007a-sortbytes-on-tpch.{txt,plans.txt,pg.plans.txt}`.
- **TPC-DS**: private clone/port 5595 (online `pg_basebackup -X fetch` from
  the SF0.25 goopg cluster, never stopping the shared `:65437` server).
  Artefact: `analysis/m0139/m0139-0007a-sortbytes-on-tpcds-goopg.txt`,
  `stats-epoch: 5d4dc56356f3d676` — identical to the baseline it is compared
  against.

## Result: zero category movement on both corpora

### TPC-H — byte-identical to the HEAD-default baseline for all 22 queries

Arm (`GOOPG_PG_SORT_RELATION_BYTES_COST=1`) vs the baseline's own committed PG
reference (`m0141-s2a-fix1-tpch.pg.plans.txt`):
```
PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
```
Identical to the baseline in every field (see the companion R108 document for
the baseline table — it is the same one). `shape-delta.sh`:
`queries=22 text-changed=22 shape-changed=0`. Every TPC-H query's numeric
annotations move (the currency substitution does change the arithmetic fed
into the Sort spill decision) but not one query's structural shape does —
notably including `sort-strategy`, the category this arm's mechanism most
directly touches: it stays at 6 on both arms.

### TPC-DS — category counts identical; two queries show text/cost movement, both already `MISSING-NODE`

Arm vs PG (`m0141-s1-tpcds-pg.txt`), same `stats-epoch: 5d4dc56356f3d676`:
```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES: join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=69 sort-strategy=75 parallelism=84 qual-placement=21 rendering=23
```
Identical totals and every category count to the baseline (see companion
document). `shape-delta.sh` against the baseline:
`queries=99 text-changed=6 shape-changed=2` (`Q4 Q11 Q36 Q64 Q70 Q86`
text-changed; Q36/Q70/Q86 are the pre-existing `SKIP_QUERYGEN` `error=3` set;
`Q4` and `Q64` are the two shape-changed queries). Verified via `--verbose`
that **both** Q4 and Q64 are `MISSING-NODE` verdicts both before and after
this arm — PG's plans for both use **Incremental Sort** (Q64) or otherwise
diverge on an estimate PG itself reports several orders differently
(`Q4`: goopg `rows=1` vs PG `rows=2`, cost ratio ~5×10⁴), neither of which
this arm's Sort-currency substitution can turn into a category-visible shape
change while the verdict stays `MISSING-NODE` (no category tag is assigned
to a `MISSING-NODE` row). This is the same M0141-S7 Incremental-Sort
dependency the R108 measurement's Q64 finding names.

## Decision: HOLD — stays default-off

Same reasoning as the companion R108 document, restated for this arm's own
evidence: zero category and match movement on both corpora, no offsetting
argument to weigh a promotion against, and this arm sits on the same B2 risk
axis (PG's `relation_byte_size` currency for Sort/`work_mem` spill decisions
is smaller than the tuple width goopg's executor actually sorts, so the same
"planner charging below what the executor needs" caution applies, and the
TPC-H SF=1 execution acceptance arm remains the unstarted precondition for
ever promoting it). Not deleted, for the same reason: a correctly-derived,
already-tested port, kept as a control for future investigation. Resolves
the arm's default-off-arm-cap expiry to **HOLD**.

## What every M0137–M0143 task report must contain (per AGENT.md)

- **Category movement**: zero, both corpora — full tables above.
- **shape-delta**: TPC-H `shape-changed=0`. TPC-DS `shape-changed=2` (Q4,
  Q64 — both pre-existing `MISSING-NODE` cases before and after).
- **Stats epoch**: TPC-DS identical (`5d4dc56356f3d676`). TPC-H uses the
  same-PG-reference control for the same reason given in the companion
  document.
- **Seam-decline census**: N/A — no join-enumeration or decorrelation path
  touched; pure Sort-cost-arithmetic currency toggle.
- **Planning route**: unchanged — `costSortRunWithWidth` is reached from the
  same existing Sort/WindowAgg costing call sites; no new route, no code
  changed (measurement only).

## Verification

- `go build ./...` — unaffected; no source file changed by this task.
- Live measurement above: TPC-H `match` unchanged at 8/22, TPC-DS `match`
  floor held at 2/99.
- Shared clusters were read from or cloned online without ever being
  stopped/started/rebuilt. All private artefacts removed after use; private
  ports (5593, 5595) are free again.

## No production diff

Confirmed: `git status --short` shows no change under `internal/` from this
task. `GOOPG_PG_SORT_RELATION_BYTES_COST` already existed at HEAD (R113);
this task only ran and recorded the measurement the recon named as missing.

## Resume points

- `GOOPG_PG_SORT_RELATION_BYTES_COST` stays default-off, closed per this
  measurement — do not re-measure without new evidence.
- Promotion precondition (unmet, unstarted, not required by this HOLD):
  `scripts/tpch-acceptance-arm.sh`'s execution gate, per the companion R108
  document's identical argument.
- M0139-0007b (Memoize's entry-byte currency) remains open and does not
  depend on this arm's disposition. With both R108 and R113 now measured and
  held, M0139-0007's inventory (recon doc, rows 1/3) is fully resolved: row 4
  (HashAggregate) was already settled before this task (M0141-S2a-fix2), and
  row 5 (Memoize) is M0139-0007b's own scope.

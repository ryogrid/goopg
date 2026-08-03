# M0125-0002 commit 2 — `cloneExprShiftIdx`, measured

2026-07-30, quiet host (load ~2, no `ci/batch`, next nightly fire 00:00).
Design: `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md`
D2 row 2 / D4.

## The prediction this run refutes

D2 row 2 said commit 2 is **"not inert … it does move plans"**, and D4 sized its
gate accordingly: units, a plan diff, a **timed 22-query TPC-H power run**, and
the SF0.5 sweep. The reasoning was sound — `cloneExprShiftIdx` returns
`(Expr, bool)` and its caller (`nl_index_join.go:363`) abandons the whole
inner-`Filter{SeqScan}` unwrap on `false`, so completing the walker *opens* the
NLI inner unwrap on conjunct shapes that declined before.

It opens nothing on either benchmark.

| instrument | baseline | result |
|---|---|---|
| `make plan-diff MODE=structural` | `plan_snapshots/m0125-0005-relsize-default-stage2` | **22/22 MATCH** |
| `make plan-diff MODE=strict-text` | same | **22/22 MATCH** (byte-identical) |
| TPC-DS SF0.5 `EXPLAIN`, 96 queries | HEAD `d4071df4` binary | **96/96 byte-identical** |
| TPC-DS SF0.5 answer sweep, 99 queries | `analysis/m0125-0003-sf05-relsize-20260730/sweep-COMPLETE-20260730-155432.txt` | **PASS 83, TIMEOUT 12, MISMATCH/CKMISMATCH/ERROR 0** |

The 20 kinds the conversion newly admits — `*IsNullExpr`, `*IsBoolExpr`,
`*CaseExpr`, `*RowExpr`, `*ExtractExpr`, `*CollateExpr`,
`*IsDistinctFromExpr`, a Plan-less `*InExpr`, and the row-independent leaves —
are common in TPC-DS query text and evidently never reach *this* site: the
conjuncts that arrive on an inner `Filter{SeqScan}` at a Semi/Anti/Inner join
were already inside the old 12-arm set. The admission test was complete enough
for both workloads and nobody knew it. That is a measurement, not a claim: had
one unwrap decision flipped, the conjunct would have moved from a leaf scan's
`Filter:` line up onto the `Nested Loop`'s, and goopg's `EXPLAIN` prints both.

## Why the answer sweep ran anyway

Because the plan gate is blind to exactly one thing the conversion changed.

The old hand-written arms **rebuilt** `*BinaryOp`, `*UnaryOp` and `*FuncCall`
from a field list rather than copying the struct, and the field lists were
stale: `BinaryOp.ResultType` ("non-empty for arithmetic with typed result",
e.g. `int2`), `FuncCall.Variadic` and `FuncCall.ReturnType` were **dropped on
every hoisted conjunct**. `shallowCloneExpr` copies the whole struct, so they
now survive. The executor reads `ResultType` for arithmetic result typing;
`EXPLAIN` renders both versions identically. Only a value gate can see it —
which is what the 50 ck-verified cells are for. Pinned by
`TestCloneExprShiftIdx_PreservesNonChildFields`.

## The one cell that moved, and why it is not commit 2's

`Q72: TIMEOUT 307 s → PASS 313 s`, everything else identical including every
value checksum.

This is **not a rescue**, and citing it as one would be wrong twice over. The
new run is *slower* (313 s vs 307 s) and still over the 300 s cap; what changed
is which side of the harness's cap check the run landed on. Q72 is the known
budget-crossing query — M0125-0005 carried it forward as "1.13× slower,
270 → 305 s, crosses the 300 s cap, UNEXPLAINED". Q72's plan is byte-identical
between the two arms (it is one of the 96), so commit 2 cannot have moved it.
The cap is a coin flip for this query and will keep flapping until whatever
made it 1.13× slower is explained.

## What was NOT run, and why

**D4 item 3's timed 22-query TPC-H power run.** With `MODE=strict-text`
reporting 22/22 byte-identical plans, the two arms are the same plan executed by
the same engine; a timing difference could only be host noise, and publishing
one would put a number in the record that a later loop would read as an effect.
Ledger row 2026-07-30. If any later commit in this series moves a TPC-H plan,
the timed run is owed again and this reasoning does not carry over.

**D4 item 2's `LABEL=tpcds-round2-head`.** Deliberately retargeted to
`m0125-0005-relsize-default-stage2`. `tpcds-round2-head` was captured before
M0125-0005 flipped the relation-size fallback default, and that flip moves
22/22 TPC-H plans on its own — diffing against it would bury commit 2's signal
under a previous commit's. D4's actual requirement (name the label; never let
`plan-gate`'s newest-by-mtime pick it) is honoured: the label is explicit.

## A false line this run found in the harness

Every per-chunk header here says `# planner-flags:
GOOPG_RELSIZE_FALLBACK=unset(off)`. That label was **stale**: M0125-0005 flipped
`defaultRelSizeFallbackStage` to 2 and made `=0` the opt-out, but
`scripts/tpcds-sf05-regression.sh` still spelled the unset case "off" — so every
SF0.5 artefact captured after the flip states the opposite of the regime it
measured. Same defect class as M0125-0011 (a report that could name a binary it
never ran). Corrected to `unset(2)` in the commit that adds this directory; the
merged report carries the correction as a note, and the four raw chunk files are
left exactly as the harness wrote them.

## Files

| file | what |
|---|---|
| `capture-plans.sh` | EXPLAIN-only plan capture, one arm per binary (the cheap instrument) |
| `head/`, `c2/`, `*.meta` | 96 plans per arm; `diff -rq` is empty |
| `run-sweep-chunks.sh` | the SF0.5 answer gate, chunked |
| `sweep-20260730-*.txt` | the four raw chunks, as written (each stamped SUBSET PROBE) |
| `sweep-COMPLETE-20260730-185223.txt` | merged 99-cell report |

Q14 is a plan-capture `PLANTIMEOUT` in **both** arms (its files are byte-equal,
so the failure is identical); it is also one of the 12 sweep timeouts.

# R2 — make the instrument able to prove the goal

*Round 2 of `../TODO.md`. Implements K7/K8, promoted ahead of the
remaining costing rounds by R1's finding
(`../r1-qpqual-index/REPORT.md` §3b).*

## 0. Why this round exists

The goal's success condition is a **proof** that every executable TPC-H
and TPC-DS query plans identically to PG 18.3. The proof instrument is
`scripts/pg-plan-parity-diff.py`. It currently cannot read the corpus.

`plan_diff` (`:1084`) short-circuits:

```python
if unknowns or (pg_kinds & set(GOOPG_UNEMITTABLE)):
    ...
    return ("MISSING-NODE", ...)
```

**One unrecognised node name forces the MISSING-NODE verdict even when
the two trees compare identically.** Counted on the R1 captures:

| corpus | MISSING-NODE | of those, citing `unknown node kind` |
|---|---|---|
| TPC-DS (99) | 62 | **56** |
| TPC-H (22) | 9 | **9** |

So 65 of the 71 "goopg is missing a node" verdicts across both corpora
are the tool failing to parse a name, not a measured divergence. A query
that already plans identically would be filed as MISSING-NODE and
counted against the goal. **Until this is fixed, "match=0 on TPC-DS" is
an unknown, not a measurement.**

This round changes no plan choice. Reclassifying a verdict the tool was
guessing at is not "arbitrarily making plans identical": the comparison
that then runs is the same comparison, on the same two texts.

## 1. The unrecognised vocabulary (census, not guesswork)

Every distinct unknown text over both corpora, with the engine that
emits it (`grep` counts over the R1 captures):

| text | class | goopg | PG |
|---|---|---|---|
| `Partial {Hash,Group,}Aggregate`, `Finalize {Hash,Group,}Aggregate` | parallel aggregate phase | yes | yes |
| `CTE <name>` (43 distinct names) | CTE header line | yes | yes |
| `Merge Append` | PG's spelling of the node | 0 | 4 |
| `SetOp {Except,Intersect}`, `HashSetOp {Except,Intersect,Union}` | set-op strategy suffix | yes | yes |
| `WindowAgg (N funcs)` | **goopg-only rendering** | 22 | 0 (PG prints bare `WindowAgg`) |

## 2. Changes

### 2.1 Tool: teach it the names both engines print

1. **Parallel aggregate phases stay first-class kinds.** Add
   `Partial|Finalize` × `Aggregate|HashAggregate|GroupAggregate|MixedAggregate`
   to `KNOWN_KINDS` **with the prefix retained in `kind`**. The prefix is
   NOT stripped: a `Partial HashAggregate` is not a `HashAggregate`, and
   goopg emitting the latter where PG emits the former is exactly the
   kind of real divergence this round must keep visible. (Census
   evidence that it is real: goopg emits `Finalize GroupAggregate` **0**
   times, PG **18**.)
2. **`CTE <name>`** → kind `CTE`, detail `<name>`, the same split
   `Index Scan using i on t` already uses. The name is a query-text
   identifier, so it belongs in detail where the existing alias
   canonicalisation can reach it.
3. **`Merge Append`** — `KNOWN_KINDS` holds `MergeAppend`, which is not
   what PG prints (`explain.c`: `"Merge Append"`). Add the spaced form.
   This makes goopg's absence of the node a *classified* divergence
   instead of a parse failure — the count may move, the truth does not.
4. **`SetOp <strategy>` / `HashSetOp <strategy>`** → kind, detail
   `<strategy>`.

### 2.2 Tool: stop mislabelling a parse failure as a missing node

Split the short-circuit. `GOOPG_UNEMITTABLE` (a node PG emits that goopg
provably cannot) keeps **MISSING-NODE**. An unrecognised name yields a
new verdict **UNPARSED**, which is neither a match nor a divergence — it
is the tool declining to answer. The goal's proof requires
`UNPARSED = 0`, and conflating the two hides exactly that requirement.

### 2.3 goopg: `WindowAgg (N funcs)` is not PG's rendering

`operators_explain.go:2483` prints `WindowAgg (%d funcs)`. PG prints
`WindowAgg` (`explain.c:1575`, `pname = sname = "WindowAgg"`). This is a
PG-faithfulness defect in goopg's EXPLAIN, not a tool problem, and the
fix belongs in goopg: the two engines' plan text should be identical
because the plans are, not because a comparator was taught to forgive a
difference. One renderer arm; the second `*optimizer.WindowAgg` case
(`:2941`) is the children accessor, not a sibling renderer — checked, so
there is no sibling to keep in sync here.

## 3. What this round must NOT do

- No normalisation that erases a structural difference. Every addition
  above is a *name*, never a shape.
- No new verdict may be produced by weakening a comparison. If a query
  moves to MATCH, the two plan texts must be adjudicated by hand and the
  adjudication recorded in the report.
- No plan, cost or selectivity change (§2.3 changes rendered text only —
  gated by the values suites, which cannot see EXPLAIN text, and by the
  executor suite, which can).

## 4. Gates

- `scripts/pg-plan-parity-diff-test.py` — the tool's own suite, green.
- `go test ./internal/executor/` — green (§2.3 touches the renderer).
- Re-run both corpora against the R1 captures with the new tool.
  Required outcome: **`UNPARSED` accounts for every verdict that moves**,
  and no query moves from SHAPE-DIFF to MATCH without a hand
  adjudication in the report.
- Values gates: TPC-H digest and TPC-DS SF0.5 sweep are re-run because
  §2.3 edits executor code, even though it cannot change a result set.
- Hand-adjudicate **at least 5 reclassified queries per corpus** by
  reading both plan texts, and publish the adjudication.

## 5. Expected outcome, stated in advance

Prediction, so the report can be checked against it rather than
rationalised afterwards: most of the 65 unknown-driven verdicts become
**SHAPE-DIFF**, not MATCH — R1's captures show real structural
divergence (goopg emits no `Finalize GroupAggregate` and no
`Merge Append` at all). A small number may become MATCH. If a large
number become MATCH, that is a signal the additions weakened a
comparison and must be re-examined before being believed.

R1's report predicted nothing and was right about nothing in particular;
this prediction exists to be falsifiable. Prior rounds in this project
repeatedly read a share as a forecast and were wrong three times — the
prediction here is directional only.

## 6. Review record

Subagent delegation is unavailable in this environment (`Task` tool not
exposed to the session; recorded at R0 and unchanged). Review performed
as a second adversarial pass by the author, with each claim checked
rather than read:

- **Census re-derived from the artefacts, not from memory**: the table
  in §1 comes from `grep -oP "unknown node kind: '[^']*'"` over both R1
  diff outputs, then per-engine counts over the two capture files. The
  `WindowAgg` asymmetry (goopg 22, PG 0) was confirmed by reading PG's
  own `explain.c:1575` in the read-only oracle, not by inferring from
  the count.
- **Short-circuit verified by reading the branch** (`:1084-1099`), not
  by assuming from the verdict name — this is the K4 discipline (the
  rev-1 error was concluding from a file without checking what runs).
  The branch does run `cmp_trees` before returning, but discards the
  verdict, which is why the categories look populated on a MISSING-NODE
  line and the verdict still says MISSING-NODE.
- **Falsification attempted on the "it's only the tool" claim**: if the
  unknown names were goopg-only renderings, teaching the tool would be
  hiding a compat defect. Two of the five classes are asymmetric
  (`Merge Append`, `Finalize GroupAggregate`) and are therefore kept as
  visible divergences; one (`WindowAgg (N funcs)`) is goopg-only and is
  fixed in **goopg**, not in the tool. Only the symmetric classes are
  taught to the comparator.

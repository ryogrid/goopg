# M0122-0012 — scan pipeline slices, part 9 (TPC-DS executor gaps, continued)

Continued from [part 8](0122-0012-tpch-scan-pipeline-slices-part8.md) (slices 80–89), which was split before the
800-line limit.

**Measurement method.** As in part 8: the SF0.25 clone `tmp/q64-clone`, with instructions:u per query
(`tmp/ds-instr.sh`) as the instrument and allocation volume (`tmp/ds-alloc.sh`) alongside it.

**Reference timings.** Gaps are ranked against release PG 18.3, whose per-query medians are in
`analysis/timing-after-planprity-match/data/tpcds-sf025-pgrel.tsv`.

Wall-clock ratios from one long warm sweep are unreliable for queries that follow a heavy one (part 8, slice 89).
Warm each query on its own, or rank by instructions.

## Slice 90 — the plain-string test and the string read inline at the comparison

**Target.** Q81, and its sibling Q30, still spent their time in the slice-89 lane. Their correlated SubPlan compares
`ctr1.ctr_state = ctr2.ctr_state` for every inner CTE row, as PG's does. That made the lane's two support calls the
largest costs in Q81:

- `indexbytebody` at 26 %: `plainCompareString`'s `strings.IndexByte` on a two-letter state code, where the call into
  the vector routine costs more than the scan;
- `Datum.StringValue` at 6.5 %: its arena arm puts it over the inlining budget (cost 143 against 80), so every read is
  a call.

**Change** (`exprnode.go`).

- **`plainCompareString`.** It tests the leading '(' or '{' and the 32-byte cut first, then scans for '/' with a byte
  loop, and now inlines. The result is the same as the `IndexByte` form: the string is under 32 bytes whenever the
  loop runs.
- **`stringValueInline(d)`.** It reads an owned string straight from `Buf`; `unsafe.String(unsafe.SliceData(Buf),
  len(Buf))` is "" for an empty or nil buffer, as `StringValue` returns. An arena-backed string calls `StringValue`. It
  inlines (cost under 80) and replaces the two `StringValue` calls in both plain-string compares: the slice-89 lane and
  `evalFastBinaryApply`'s branch.

**Why the plain test stays.** For a character-string pair (payload bit 4), most non-plain strings compare as text on
the general path too:

- `textShapeAmbiguous` sends shape-alike pairs to `binaryTextComparison`, which is `strings.Compare` after the bpchar
  trim.

The exception is a pair that is not shape-ambiguous but has one side carrying a '/':

- it reaches `evalPgLSNBinary` on the untrimmed values;
- a padded bpchar that parses as a pg\_lsn there would compare numerically.

So the guard is required, and only its cost changed.

**Pins** (in `string_compare_inplace_test.go`).

- `TestPlainCompareStringMatchesIndexByteForm` checks the byte loop against the `IndexByte` form, with a '/' at every
  position, leading and non-leading brackets, and lengths 30 to 33. Starting the loop at index 1 fails it.
- `TestStringValueInlineMatchesStringValue` covers owned, empty, nil-buffer and arena-backed strings. Returning "" for
  the arena arm fails it.
- Slice 89's `TestStringCompareInPlaceMatchesInterpreted` still passes.

**Measured** against `e8bf90456` (instructions:u per run):

| query | before | after | change |
|---|---|---|---|
| Q81 | 36.90 G | 31.34 G | −15.1 % |
| Q30 | 8.45 G | 7.24 G | −14.3 % |
| Q59 | 12.09 G | 11.86 G | −1.9 % |
| Q2 | 8.93 G | 8.88 G | −0.6 % |

Two steps make up Q81's −15.1 %: the byte loop alone was −9.1 %, and the inlined read another −3.4 % before its last
budget trim.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

## Slice 91 — a correlated sublink over a CTE scan is lowered, so its result cache keys on the parameters

**Target.** TPC-DS Q1, Q30 and Q81 filter a CTE scan inside a correlated scalar sublink:
`ctr1.ctr_total_return > (SELECT avg(ctr_total_return) * 1.2 FROM ctr ctr2 WHERE ctr1.k = ctr2.k)`. After slice 90,
Q81 still ran 31 G instructions.

EXPLAIN ANALYZE showed why: `SubPlan 2 (calls=6893 rebuilds=6893 hits=0 misses=6893)`. The sublink was not lowered to
PARAM\_EXEC slots (D4.1, `subplan_lower.go`), so its result cache was keyed by the whole outer row (`subqueryCacheKey`)
and missed on every call. Each call rescanned the whole CTE.

**Root cause.** The lowering walk (`lowerTraverseNodeHooked`) had no CTEScan arm, and it bails on every node kind it
does not model, as its soundness contract requires. A sublink over a CTE scan therefore always stayed on the
OuterRows stack path.

The CTE body (`CTEScan.Child`) cannot simply be walked, for two reasons:

- it is shared by every reference to the CTE (the executor fills its row cache once per CTE), so a rewrite inside it
  would rewrite every reference;
- Q81's body holds a parameterised nested loop whose inner index probe reads the join's left row as a Level-1
  `OuterColumnRef`. That reference is local to the body, but a flat "no outer refs" test (`excludedRefsWithin(body,
  0)`, the first attempt) rejects it.

**Change** (`internal/optimizer`).

- **`selfContainedPlan(plan, frames)`** (subplan\_lower.go) is a non-mutating check on the lowering walk itself, so an
  unmodelled kind still refuses:
  - an `OuterColumnRef` must have `Level <= frames`;
  - a lateral Join's right child is walked one frame deeper, as `keptRebase.rebaseNode` walks it, because the join
    pushes its left row there;
  - a nested sublink's plan is walked one frame deeper, and an IN's operand and list in the current frame;
  - an `ExecParamRef` refuses, because its slot's binder cannot be told from here.
- **The CTEScan arm** of `lowerTraverseNodeHooked` (subplan\_lower\_walk.go) returns `selfContainedPlan(x.Child, 0)`
  and never enters the body. The analysis and rewrite phases, and the keptRebase pass, all treat a self-contained CTE
  scan as a leaf.

PG's `SS_replace_correlation_vars` turns every correlated Var into a PARAM\_EXEC regardless of what lies below. This
brings goopg's lowering closer to it, and executor caching is unchanged.

**Plans are unchanged.** Census on this tree: TPC-H 22/22, SF0.25 99/99 and SF1 99/99 MATCH, and the captured plan
text is byte-identical to the previous capture's (only headers differ). The fire-set gate saw no changed query.
`EA-RATCHET: PASS`.

**Pins.**

- `TestLowerScalarSublinkOverCTEScan` (optimizer): a sublink over a MATERIALIZED CTE scan is lowered to one param
  whose Arg is the host's column, and the CTE body carries no ExecParamRef or OuterColumnRef afterwards.
- `TestSublinkOverCorrelatedCTEStaysOnStack`: a CTE defined inside the sublink over the outer row keeps the sublink
  correlated and unlowered. Lowered, it would have no params and would be marked non-correlated, so one cached result
  would serve every outer row. A CTEScan arm that accepts without the check fails this test.
- `TestSelfContainedPlan` covers ten plan shapes: local and escaping refs, lateral and plain joins, nested sublinks,
  ExecParamRef, and an unmodelled node.
- `TestCorrelatedSublinkOverCTEValues` (executor) checks Q81's shape over 60 rows, including NULL keys and bpchar
  padding, against values computed in Go, for both the lowered query and the stack-path query. The accept-always
  mutation makes the stack-path query return wrong rows there too.
- `selfContainedPlan` is pinned `walkerPending` in `exprSwitchInventory`, with a ledger row.

**Measured** against `4cf358ae6`:

| query | instructions before | instructions after | warm wall time | release PG |
|---|---|---|---|---|
| Q1 | 99.2 G | 1.00 G | 0.108 s | 7.20 s |
| Q81 | 31.4 G | 0.99 G | 0.102 s | 2.29 s |
| Q30 | 7.25 G | 0.49 G | 0.054 s | 0.52 s |

Q81's SubPlan now shows `hits=6841 misses=52`, one miss per distinct state.

Values are identical:

- sorted full output of Q1, Q30 and Q81 equals the `:65438` reference (SELECT only);
- sf025 PASS=99 (61 ck), and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Still bailing** (ledger row). A sublink whose plan, or whose CTE body, holds another unmodelled kind still stays on
the stack path: WindowAgg, SetOp, RecursiveUnion and ProjectSet.

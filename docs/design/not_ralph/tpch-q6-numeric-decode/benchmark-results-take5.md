# Take 5 — killing the per-row row-copy and the whole-row deform

**Status:** implemented and measured
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Baseline for this round:** `76766dfe3` (take 4 — the numeric-decode fix in [DESIGN.md](DESIGN.md))
**Oracle:** PostgreSQL 18.3, TPC-H SF=1, port 65432
**Raw artefacts:** `tmp/take4/runs/` (not committed; §7 gives the commands)

---

## 1. Summary

[DESIGN.md §9](DESIGN.md#9-what-this-does-not-fix) listed what the numeric fix
deliberately left on the table. This round takes the top two items, plus a
third the first two exposed:

| # | target | before | after |
|---|---|---:|---:|
| 1 | `cloneRowOwned` — every scanned row deep-copied | 26.6 % of CPU | **1.16 %** |
| 2 | whole-row deform — all 16 `lineitem` columns decoded | 16 of 16 | **6 of 16** on 98 % of rows |
| 3 | `NumericConst` re-parsed per row through `math/big` | 5.6 % CPU / 22.8 % of allocations | **gone** |

**Q6 is 1.85× (serial) / 2.18× (parallel) faster than the take-4 baseline**, with
**1.86× fewer instructions per row** and **3.2× fewer allocations per query**.
The result is bit-identical (`102513054.4896`) and `tpch-spotcheck` passes.

Across both rounds of this session, from `832822594`:

| | session start | after take 4 | **after take 5** | PG 18.3 | gap now |
|---|---:|---:|---:|---:|---:|
| Q6 serial | 23.40 s | 11.51 s | **6.63 s** | 0.9905 s | **6.7×** (was 23.6×) |
| Q6 parallel | 5.235 s | 2.784 s | **1.22 s** | 0.2025 s | **6.0×** (was 25.9×) |
| allocations / query | 291.6 M | 60.1 M | **18.8 M** | — | −93.6 % |

---

## 2. Root cause of each target

### 2.1 `cloneRowOwned` — the copy happened before the filter, not after

`seqScanOp.Next` deep-copied **every visible tuple** at
`operators_storage.go:2036`, before yielding it to the `filterOp` above:

```go
row = cloneRowOwned(row)   // detach arena-backed Datums before the page RUnlock
```

The copy itself is not gratuitous — it exists so the yielded slot stays readable
after the page RLock is dropped, which a concurrent `UPDATE` would otherwise
tear. The waste is *when* it happened. Q6's filter keeps ~1.9 % of `lineitem`,
so goopg was allocating a fresh 16-Datum row and re-materialising five
arena-backed string columns for 6.0 M rows in order to hand 5.9 M of them to a
filter that discarded them.

The profile split it as `acquireRow` 39.2 %, `Datum.MaterializeArena` 28.5 %,
and 32.3 % in the copy loop itself.

PostgreSQL does not have this problem because `SeqNext` returns a slot still
pointing into the shared buffer and `ExecScan` runs `ExecQual` on it; only a
surviving tuple is materialised.

### 2.2 Whole-row deform — nothing told the scan what was needed

`seqScanOp.cols` is `p.Table.Columns` (`operators_storage.go:1320`) — the whole
relation, always — and `DecodeRowIntoMctxPGTupleStyled` loops over all of it.
Q6 reads 4 of `lineitem`'s 16 columns; PostgreSQL's `slot_getsomeattrs` stops at
the highest referenced attribute.

There is no column-pruning infrastructure in the repo to reuse: no
`NeededColumns`, no column mask, no projection push-down. Building a static
"which columns can any ancestor read" analysis would mean an expression walker
over arbitrary parent plans — and this codebase has a documented history of
walkers that silently miss an arm (`internal/optimizer/exprwalk_inventory_test.go`
tracks the pending ones; the deferral ledger records a 11→18 `Expr`-kind
expansion). A missed arm there is a **wrong answer**, not a slow one.

§3 takes a design that needs no such analysis.

### 2.3 `NumericConst` — a constant, parsed six million times

`evalExpr`'s literal arm (`expr.go:550`) was:

```go
case *optimizer.NumericConst:
    m, s, err := parseNumeric(x.Value)     // math/big.Int.SetString
```

`parseNumeric` goes straight to `math/big`. So `l_quantity < 24` re-parsed the
text `24` into a `big.Int` **on every row**, as did `0.04` and `0.06`. The
sibling heap-decode path (`codec.go`) has had int64 fast paths for this since
the round-5 TPC-H work; the literal path never got them. A textbook instance of
the repo's own `pattern_sibling_paths_must_agree`.

This was 5.6 % of CPU and **22.8 % of all allocations** after §3 landed — it
only became visible once the row-copy stopped dominating.

---

## 3. Design

### 3.1 Evaluate the predicate inside the scan, and deform in two phases

The scan already receives the predicate of a `Filter` sitting directly above it
— `executor.go:133` hands it down for the GiST/GIN spatial-SSI path. The same
expression is reused:

```
decode cols[0 : MaxCols]        ← only the columns the predicate reads
evaluate the predicate
  ├─ false → skip the tuple entirely (no further deform, no copy)
  └─ true  → decode cols[MaxCols : 16], resuming at the returned byte offset,
             then continue down the unchanged path (detoast → cloneRowOwned → …)
```

**This is what makes the whole thing safe, and it is the key point of the
design:** the max-attnum analysis only ever has to cover *the predicate the
scan itself holds*. It never has to reason about what a parent plan might read,
because the partially-deformed row is passed to nothing but that predicate, and
any row that survives is fully deformed before it is yielded. §2.2's dangerous
analysis is not needed at all.

Two supporting guarantees:

- **The prefilter can only remove rows the `Filter` would remove.** `filterOp`
  is left in place and still evaluates the same predicate on survivors. The keep
  condition is copied character-for-character from `filterOp.Next`:
  `!v.IsNull() && v.Kind == KindBool && v.BoolValue()`.
- **Errors are not raised from the prefilter.** On an evaluation error the row
  is kept and `filterOp` raises it, so error text, position and ordering are
  identical to a build with the prefilter disabled.

### 3.2 The whitelist, and which way it fails

`prefilterSafeExpr` (`scan_prefilter.go`) is a **whitelist**: `ColumnRef`, the
literal kinds, `BinaryOp`, `UnaryOp`, `CastExpr`, `IsNullExpr`, `IsBoolExpr`,
`IsDistinctFromExpr`. Anything else — `FuncCall` (volatility unknown),
`SubqueryExpr`/`ExistsExpr`/`InExpr`, `OuterColumnRef`, `ParamRef`, `CTIDExpr`,
`RowExpr`, `LikeEscapePattern` — returns false and **disables the prefilter
entirely**.

The direction is the whole point. Because the predicate is evaluated twice on
survivors it must be deterministic and side-effect free; an unrecognised node
must cost speed, never correctness. `TestPlanScanPrefilterWhitelist` pins that,
including a `FuncCall` buried in an otherwise-safe subtree.

### 3.3 Resumable range decode

`DecodeRowRangeIntoMctxPGTupleStyled(dst, cols, data, bitmap, natts, sctx, st,
from, to, off) (int, error)` decodes a column window and returns the byte offset
reached. `DecodeRowIntoMctxPGTupleStyled` is now the `from=0, to=len(cols),
off=0` case, so the single-pass path is unchanged by construction.

A physical tuple carries no per-column offset array, so a **suffix** can be
skipped but a prefix cannot, and the returned offset is what makes resumption
exact — thread it back wrong and the second half decodes garbage rather than
failing. `TestDecodeRowRangeResumeEqualsFullDecode` checks every split point of
a 7-column row (mixed `int4`/`numeric`/`text`/`int8`/`bool`) against the
single-pass decode.

### 3.4 Disarming

Set at build time, cleared in `Open` when any post-decode row rewrite is live,
because each mutates the row *after* `cloneRowOwned` and would make the
prefilter judge different values than `filterOp` sees:

| condition | why |
|---|---|
| `gistSSIIdxOID != 0` / `ginSSIIdxOID != 0` | per-tuple SSI bookkeeping runs after decode on matching rows; a skipped row must not miss it |
| any enum column | enum injection rewrites `KindString` → `KindEnum` after the copy |
| `typeACLColIdx >= 0` / `attrACLColIdx >= 0` | `aclitem` blobs are rendered to text after the copy |
| toasted value in the prefix | judged un-detoasted; falls back to full deform + `filterOp` alone |

> **This is where the first implementation was wrong, and the bug is worth
> recording.** The disarm test was `o.enumTypes != nil` — but `enumTypes` is
> allocated for *every* scan (one slot per column, `nil` where the column is not
> an enum, `operators_storage.go:1451`), so it is never nil and the prefilter
> was disarmed **unconditionally**. It built, passed every test, produced
> correct results, and delivered a fraction of the expected win. It was caught
> by instrumenting the arming decision rather than by trusting the wall clock:
> a temporary `PREFILTER ARMED maxcols=%d ncols=%d` line printed
> `maxcols=6 ncols=16`, confirming the intended cut, only after the condition
> was corrected to scan for a non-nil entry. Serial went 10.4 s → 6.1 s the
> moment it was fixed.

### 3.5 The literal fast path

`evalExpr`'s `NumericConst` arm now tries `parseNumericFastInt` and then
`parseNumericFastScale(value, -1)` (−1 = accept any scale) before falling back
to `parseNumeric`. The fast paths are a strict subset — they reject exponents,
underscores and >18 significant digits — so anything they decline behaves
exactly as before.

One refinement was needed. `parseNumericFastScale` capped on *total* digit
characters, and PostgreSQL constant-folds Q6's `0.05 + 0.01` to
**`0.060000000000000005`**, which spells 19 digit characters — 18 of them after
a leading `0` that carries no magnitude. It failed the cap and fell into
`math/big` once per row. Leading zeros are now stripped before the cap, which
is value-preserving; the mantissa `60000000000000005` fits `int64` with three
orders of magnitude to spare.

`TestNumericConstFastPathsMatchBigIntPath` asserts the fast paths agree with the
`math/big` path on a corpus that includes that literal, both signs, trailing
zeros, and the forms that must still decline (`1e5`, `1_000`, 23-digit values).

---

## 4. Results

### 4.1 Wall clock — alternating A/B, fresh server per arm

Fresh server per arm holds server age constant (the "sweep-tail collapse"
confound that has twice mimicked a regression here). Two rounds, A/B/A/B.

| round | mode | take 4 baseline | take 5 | speedup |
|---|---|---:|---:|---:|
| 1 | serial | 11.383 s | **6.972 s** | 1.63× |
| 2 | serial | 12.228 / 11.305 s | **7.615 / 6.719 s** | 1.68× |
| 1 | parallel | 2.655 / 2.669 s | **1.251 / 1.239 s** | 2.14× |
| 2 | parallel | 2.673 / 2.675 s | **1.203 / 1.203 s** | 2.22× |

Ranges are disjoint in every round and mode. The serial arm's first read in each
round is still warming; the fully-warmed serial figure comes from §4.2's 20-rep
run: **12.29 s → 6.63 s = 1.85×**.

Against PostgreSQL:

| | take 4 | take 5 | PG 18.3 | gap before | gap after |
|---|---:|---:|---:|---:|---:|
| serial | 12.29 s | **6.63 s** | 0.9905 s | 12.4× | **6.7×** |
| parallel | 2.66 s | **1.22 s** | 0.2025 s | 13.1× | **6.0×** |

**Result value identical on every arm: `102513054.4896`.**

### 4.2 Instructions per row — both arms back-to-back

Measured back-to-back in one host state, because the host's clock had drifted
(4.37 → ~3.0 GHz) over the session and absolute counts are not comparable across
sessions. Each arm: fresh server, 3 warm-up queries, then a 60 s `perf` window
over a 20-rep serial stream.

| | take 4 baseline | take 5 |
|---|---:|---:|
| per-query serial (5 reps) | 12.455 / 12.243 / 12.230 / 12.280 / 12.240 s | **6.626 / 6.676 / 6.572 / 6.665 / 6.605 s** |
| mean | 12.29 s | **6.629 s** |
| `instructions:u` (60 s) | 759,606,628,568 | 757,160,142,263 |
| rows scanned in window | 29.30 M | **54.33 M** |
| **instructions / row** | **25,925** | **13,935** |
| ratio | — | **1.86× fewer** |

The instruction ratio (1.86×) and the wall ratio (12.29 / 6.629 = 1.85×) agree
to within 1 %, measured independently — which is the check
[DESIGN.md §4.3](DESIGN.md) could not offer, having derived one from the other.

### 4.3 Allocations

From `runtime.MemStats` across exactly four Q6 runs on a fresh server — the same
method as DESIGN.md §5.1, and host-state independent.

| | session start | take 4 | **take 5** |
|---|---:|---:|---:|
| allocations / query | 291,597,828 | 60,071,326 | **18,802,320** |
| allocated bytes / query | 10.895 GB | 8.046 GB | **2.315 GB** |
| vs previous round | — | −79.4 % | **−68.7 %** |
| vs session start | — | −79.4 % | **−93.6 %** |

Bytes finally fall as steeply as counts (−71 % this round) because the removed
work is the row copy itself, not just small satellite allocations.

### 4.4 Where the time goes now

30 s CPU profile, parallel run:

| | take 4 | take 5 |
|---|---:|---:|
| `cloneRowOwned` | 26.64 % | **1.16 %** |
| `Datum.MaterializeArena` | 8.59 % | below cut |
| `parseNumeric` (+ `math/big`) | 2.95 % | **below cut** |
| `evalExprSlot` | 17.94 % | 35.17 % ← now the top item |
| `DecodeRowRange…` (6 of 16 cols) | 27.83 % (16 of 16) | 27.98 % |
| `storage.PageGetHeapTuple` | — | 19.06 % |
| `strings.ToLower` (type-name switch) | 5.68 % | 5.27 % |

Wait-event sampling: **53 of 53 samples `active` with an empty wait event** —
still pure CPU, no I/O or lock waiting.

`evalExprSlot` becoming the largest item is the expected and correct outcome:
predicate evaluation over 6 M rows is the irreducible work of this query, and
it is now what the query mostly does. The 6-column deform holds roughly its
previous *share* while doing 37 % of the columns, because the rest of the query
shrank around it.

---

## 5. Correctness

| check | result |
|---|---|
| Q6 result bit-identical | ✅ `102513054.4896` on all four A/B arms |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | ✅ pass |
| `scripts/tpch-spotcheck.sh` | ✅ `RESULT=PASS` — Q12 = 2, Q13 = 34 |
| `go test ./internal/executor/ ./internal/nodes/ ./internal/optimizer/` | ✅ pass |
| `go test -race ./internal/executor/` | ✅ pass |
| new tests | `TestPlanScanPrefilterWhitelist` (15 cases), `TestDecodeRowRangeResumeEqualsFullDecode` (every split point), `TestNumericConstFastPathsMatchBigIntPath` |
| pre-commit pgbench smoke | runs on every commit; never bypassed |

`gofmt`: the edited files are clean except `internal/executor/codec.go` and
`expr.go`, which were already reported at `HEAD` before this change (the repo's
go1.25 baseline vs a newer local `gofmt`); the reported hunks are import
ordering and pre-existing indentation, none in the edited regions, so they were
left alone per `CLAUDE.md`.

---

> **Superseded in part (2026-08-30):** the `PageGetHeapTuple`/`ParseHeapTuple`
> allocations below were removed in
> [benchmark-results-take6.md](benchmark-results-take6.md) — allocations/query
> 18.8 M → 0.80 M, Q6 parallel 1.21 s → 1.03 s. `evalExprSlot` is unchanged and
> is re-characterised there (31.88 % is cum; flat is 14.19 %).

## 6. What is left

| item | measured | note |
|---|---:|---|
| `evalExprSlot` | 35.17 % | interpreted, per-conjunct, per-row. The standing answer is expression compilation (PG's `ExecReadyExpr` / JIT). Largest remaining item by far. |
| `storage.PageGetHeapTuple` + `ParseHeapTuple` | 19.1 % CPU, **57.9 %** of allocations | the per-tuple header parse allocates; a slot-shaped tuple view would remove it |
| type-name `strings.ToLower` per value | 5.27 % | `decodePhysicalPGValueMctxStyled` and `physicalPGTypeAlign` lowercase `t.Name` for every value; a per-column type code resolved once in `Open` removes it |
| `bytes.TrimSpace` in the numeric probe | 3.01 % | introduced in take 4; a first-byte gate already cut most of it, the residual is the `TrimSpace` call itself |
| prefix-only deform | — | the cut is a *suffix* skip. Q6 needs attnums 1, 3, 4, 6 but must still decode 2 and 5, because a physical tuple has no offset array. PG has the same constraint. |
| prefilter reach | — | armed only under a `Filter` directly above a `SeqScan`. Index scans, and filters separated by another node, do not get it. |

---

## 7. Reproduction

```bash
go build -o tmp/take4/goopg-take5 ./cmd/goopg          # private path: tmp/goopg-bench-bin is the nightly lane's
tmp/take4/start-goopg.sh tmp/take4/goopg-take5 t5      # cgroup-capped start on 65433
tmp/take4/ab.sh                                        # alternating A/B, fresh server per arm
tmp/take4/perf-ab.sh                                   # back-to-back instructions/row
TAG=q6-take5final SECS=30 tmp/take4/profile-q6.sh      # pprof + wait events + perf
GOOPG_BIN=$PWD/tmp/take4/goopg-spotcheck5 scripts/tpch-spotcheck.sh
```

Environment is DESIGN.md §3 unchanged: `GOGC=off`, `GOMEMLIMIT=12GiB`, cgroup
soft cap above `GOMEMLIMIT`, `perf stat -p PID … sleep N` with **no** trailing
`--`.

## 8. Changed files

| file | change |
|---|---|
| `internal/executor/scan_prefilter.go` | **new** — `scanPrefilter`, `planScanPrefilter`, the whitelist walker, `evalPrefilter` |
| `internal/executor/scan_prefilter_test.go` | **new** — whitelist, split-decode and literal-agreement tests |
| `internal/executor/codec.go` | `DecodeRowRangeIntoMctxPGTupleStyled`; the whole-row entry point becomes a wrapper |
| `internal/executor/operators_storage.go` | `prefilter`/`prefilterSet` fields, `decodeScanRowRange`, the two-phase deform in `Next`, the `Open` disarm |
| `internal/executor/executor.go` | arm the prefilter at both `Filter`-over-`SeqScan` build sites |
| `internal/executor/expr.go` | `NumericConst` takes the int64 fast paths |
| `internal/executor/numeric.go` | `parseNumericFastScale` strips leading zeros before the 18-digit cap |
| `internal/executor/toast.go` | `needsDetoastPrefix` |

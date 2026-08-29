# M0134-0175 — TABLESAMPLE (SYSTEM / BERNOULLI)

**Status:** accepted (2026-08-29)
**Case:** `postgres/src/test/regress/sql/tablesample.sql`
**Result:** `not-tried` → `failed`; 402 → **304** diff lines, `^+ERROR` 46 → **6**,
`^-ERROR` 10 → **3**.

## What was missing

`TABLESAMPLE` did not exist. The keyword was in the generated keyword lists
(`grammar/kwlists_gen.y:425`, correctly categorised as
`type_func_name_keyword`) and had a token number, but **no production consumed
it** — `internal/parser/keyword_reachability_test.go` carried it on the
`notYetPortedKeywords` allowlist as "P1.2 FROM ... TABLESAMPLE". Every one of
the case's 30 sampled statements died at `syntax error at or near
"TABLESAMPLE"`, and 14 more were collateral `current transaction is aborted`.

## The property that made an exact port possible

The decisive discovery of this loop, and the reason the result is exact rather
than statistical: **PostgreSQL's two built-in sampling methods are deterministic
hash functions, not PRNG streams.**

- `bernoulli_nextsampletuple` (`access/tablesample/bernoulli.c`) hashes the
  3-word array `{blockno, offset, seed}` with `hash_any` and admits the tuple
  when the result is below a cutoff.
- `system_nextsampleblock` (`access/tablesample/system.c`) hashes the 2-word
  array `{blockno, seed}` the same way and admits the whole block.
- The cutoff is `rint((PG_UINT32_MAX + 1) * percent / 100)`, held in a `uint64`
  so that 100% is representable — upstream notes this "gives strictly correct
  behavior at the limits of zero or one probability".
- The seed is `hashfloat8(REPEATABLE value)` (`nodeSamplescan.c:270`), whose
  zero short-circuit is what makes `REPEATABLE (0)` yield seed 0 on every
  machine. Upstream cites exactly this as the regression-testing property.

goopg already carried the same Jenkins hash as `pgHashBytesExtended`
(`hash_partition.go`, whose low 32 bits ARE `hash_any`), so no new hash
primitive was needed — only the two comparison loops.

`internal/executor/tablesample_test.go` pins the port against the row sets in
`expected/tablesample.out` directly: `SYSTEM (50) REPEATABLE (0)` → 3,4,5,6,7,8;
`BERNOULLI (50) REPEATABLE (0)` → 4,5,6,7,8; `BERNOULLI (5.5) REPEATABLE (0)`
→ 7. **All three match byte-for-byte.**

## What landed

| layer | file | change |
|---|---|---|
| AST | `internal/parser/ast.go` | `RangeTableSample` + `RangeVar.TableSample`. Upstream wraps the relation in a separate node; goopg's FROM item is a flat `RangeVar` value, so the descriptor hangs off it. |
| grammar | `grammar/pg_grammar.y` | `tablesample_clause` / `opt_repeatable_clause` (gram.y:14001) and one new `base_table_ref` arm. **Zero new conflicts** — still the pinned 59. |
| ctor | `internal/parser/yacc_ctors.go` | `NewRangeTableSample`, downcasing the method as PG's scanner does. |
| planner | `internal/optimizer/plan.go`, `planner.go` | `TableSampleSpec` (args resolved to `optimizer.Expr`) on `SeqScan`; resolved once, above the inheritance/partition expansion, so every Append leaf becomes a Sample Scan. |
| executor | `internal/executor/tablesample.go` (new) | the two samplers, the cutoff, the seed derivation, and the four validation errors. |
| executor | `internal/executor/operators_storage.go` | two hooks in `seqScanOp`: block acceptance in the block-advance path, offset acceptance in the tuple loop. |
| EXPLAIN | `internal/executor/operators_explain.go` | `Sample Scan on <rel>` label + the `Sampling:` line (`show_tablesample`, explain.c). |

### Two design decisions worth their rationale

**One scan node, not two.** Upstream has a distinct `SampleScan` plan node whose
only structural difference from `SeqScan` is the sampler callback pair. goopg
keeps one node and switches on `TableSample != nil`, so TABLESAMPLE composes
with everything the scan already does — visibility, SSI conflict-out, ring
buffers, parallel block claiming — instead of duplicating it. The EXPLAIN label
therefore becomes a property of the node rather than of its type
(`seqScanLabel`).

**The sampler is consulted BEFORE visibility checking**, matching upstream:
`bernoulli` hashes a line-pointer *offset* whether or not that line pointer
holds a live tuple. A dead tuple consumes its slot in the sample rather than
shifting later tuples into it. Sampling after visibility would produce a
plausible-looking but different — and non-reproducible — result set.

**The sampler is stateless.** Upstream keeps `sampler->nextblock` in the sampler;
goopg passes the block cursor in as an argument, which means `rewind()` needs no
reset and a LATERAL rescan is correct for free.

### Errors, all now byte-identical to the oracle

`tablesample method foobar does not exist` (42704, caret on the method name),
`TABLESAMPLE parameter cannot be null` (2202H), `TABLESAMPLE REPEATABLE
parameter cannot be null` (2202G), and `sample percentage must be between 0 and
100` (2202H) for all four of −1/200 × SYSTEM/BERNOULLI. The
derived-table rejection (`(SELECT …) as q TABLESAMPLE …` → syntax error) also
matches, and falls out of attaching the clause to `relation_expr_opt_alias`
rather than to `base_table_ref` — the same placement upstream uses, for the same
reason.

**Check order is load-bearing:** upstream resolves the method in the PARSER and
the percentage in the EXECUTOR, so `TABLESAMPLE FOOBAR (-1)` reports the unknown
method, not the bad percentage. `buildTableSampler` preserves that precedence
explicitly, and a revert-check confirmed the guard catches its inversion.

## The blocker the case then exposed: fillfactor is never applied

With the sampler exact, the end-to-end row sets still diverge — and the cause is
**not** in the sampling code. `test_tablesample` is created
`WITH (fillfactor=10)`; PG packs its ten ~224-byte rows 3 to a page across 4
blocks, goopg packs all ten into **one** block. `SYSTEM (50) REPEATABLE (0)` then
returns 0 rows instead of 6, because goopg's only block (0) hashes to
4017521692, above the 2^31 cutoff — arithmetic that is *correct* over the wrong
page layout.

`catalog.Table.Fillfactor` is parsed, bounds-checked, persisted to
`pg_class.reloptions`, round-tripped by pg_dump, and consumed by the **cost
model** (`internal/optimizer/relsize.go:575`). It has **no consumer in the heap
insert path**: `operators_storage.go` calls `storage.PageAddHeapTuple` and only
moves to a new page when the current one is physically full. Upstream's
`RelationGetBufferForTuple` (`access/heap/hio.c`) instead compares
`PageGetHeapFreeSpace` against `RelationGetTargetPageFreeSpace(relation,
HEAP_DEFAULT_FILLFACTOR)`.

This is the "declared but unconsumed" pattern again, and its consequence reaches
well past this case: **`fillfactor` cannot do the one thing it exists for** —
reserving space so an UPDATE can stay on-page as a HOT update. Filed as
M0134-0175a with a ledger row.

## Remaining buckets (case PARKED)

| bucket | size | note |
|---|---|---|
| A — fillfactor ignored at INSERT | ~140 lines | M0134-0175a. Dominant; governs every REPEATABLE row set and every cursor FETCH. |
| B — LATERAL outer column in a sample argument | ~40 lines / 5 errors | M0134-0175b. `bernoulli (pct)` → `column "pct" does not exist`. |
| C — TABLESAMPLE on a view / CTE must raise 42809 | ~25 lines | M0134-0175c. goopg silently samples the inlined body. |
| D — `('1'::text < '0'::text)::int` | 1 error | M0134-0175d. Missing bool→int cast arm; engine-wide, 4th instance of the pattern. |
| E — inheritance-child EXPLAIN alias (`person person_1`) | 4 lines | Pre-existing naming gap, unrelated to TABLESAMPLE. |
| F — `\d+` echoes the view's raw text | ~6 lines | Already filed as M0134-0169a (`pg_get_viewdef` does not deparse). |

**Re-arm trigger:** land M0134-0175a (fillfactor at insert) and re-run
`scripts/pg-regress-runner.sh --verbose tablesample`. Bucket A alone should
collapse most of the residual, because the sampler underneath it is already
proven exact.

## Gates

`make gen-parser` (59 conflicts, unchanged); parser / executor / optimizer /
catalog / storage suites PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=34 — canonical); `make check-testport-inventory` +
`make regen-testport` PASS.

**Golden corpus as the review artifact** (playbook §12): regenerating
`internal/parser/testdata/parity_goldens.txt` changed 450 lines, and stripping
the new `,TableSample=∅` substring reproduces the previous file **byte for
byte** — proof that the grammar change altered no other pinned AST.

Three guards were revert-checked: swapping bernoulli's hash word order and
inverting the method/percentage check order each fail the suite. A third
(`math.RoundToEven` → `math.Round`) does **not** fail, and that is recorded
rather than hidden: the two differ only at exact `.5` cutoff boundaries, which
none of the pinned percentages reach. `RoundToEven` is still the faithful
choice, since C's `rint()` is round-half-even under the default rounding mode.

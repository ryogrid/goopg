# M0139-0007b — port PG's Memoize entry-byte currency (absorption + measurement)

Status: accepted

## Task

The last open sub-task of the banner's top-priority "M0141-S2a-fix and
M0139-0007 — the costing-order unblock" line
(`.ralph/fix_plan.md` M0139-0007b). Unlike M0139-0007a (which measured two
already-built arms), this is **new absorption code**: give
`joinpathsmemoize.go`'s `costMemoizeRescan` PG's `cost_memoize_rescan` currency
for its cache-entry byte estimate
(`postgres/src/backend/optimizer/path/costsize.c:2541-2578`), per B2's
absorption principle (`AGENT.md` §"Plan-parity harness" — "same statistics,
same plan; irreducible representation differences must be absorbed by giving
the cost model the PG-equivalent quantity").

## Derivation (written before measuring, per B2 rule 1)

PG's `cost_memoize_rescan` prices one cache entry as (costsize.c:2560-2568):

```c
est_entry_bytes = relation_byte_size(tuples, width) +
    ExecEstimateCacheEntryOverheadBytes(tuples);
foreach(lc, mpath->param_exprs)
    est_entry_bytes += get_expr_width(root, (Node *) lfirst(lc));
```

goopg's pre-existing `costMemoizeRescan` (before this task) priced the whole
thing through `hashsize.EntryBytes`, goopg's own executor/`kvcache` currency —
the same currency M0139-0007 named as the general problem (a Go `Datum`/map
entry is not PG's `MinimalTuple`/hash-table entry). Three terms, three
substitutions:

1. **`relation_byte_size(tuples, width)`.** PG's own function
   (`costsize.c:6453`, `tuples * (MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader))`)
   is already ported verbatim as `pgRelationByteSize` (`sort_pgrelationbytes.go:35`,
   landed for R113/Sort and reused unmodified here — same ruler, no
   re-derivation). Its `width` input is PG's pathtarget width, which goopg
   already threads through `pathWidth(p)` (`path.go:691`, `p.OutputWidth` else
   `p.Rel.Width`) for the identical Sort/Window absorption sites
   (`joinpathsmerge.go:493`, `partialsortpaths.go:258,288`,
   `windowsetoppaths.go:200`). This is a straight reuse, not new derivation:
   Memoize wraps a `Path` the same way Sort does, so the same accessor gives
   the same PG-equivalent quantity.
2. **`ExecEstimateCacheEntryOverheadBytes(ntuples)`** (`nodeMemoize.c:1171-1176`)
   is `sizeof(MemoizeEntry) + sizeof(MemoizeKey) + sizeof(MemoizeTuple) *
   ntuples`. These are PG C-struct sizes, not a per-column statistic, so they
   are a **named, portable constant** (B2 rule 2 — the quantity exists in PG).
   Computed from the struct definitions themselves
   (`nodeMemoize.c:94-123`, `ilist.h:136-141`) on PG's only target layout
   (LP64: 8-byte pointers, 8-byte MAXALIGN):
   - `MemoizeTuple{MinimalTuple mintuple; struct MemoizeTuple *next;}` — both
     fields are pointers (`MinimalTuple` is `typedef struct
     MinimalTupleData *MinimalTuple`, `htup.h`) → 16 bytes, no padding.
   - `MemoizeKey{MinimalTuple params; dlist_node lru_node;}` —
     `dlist_node` is two pointers (16 B) → 8 + 16 = 24 bytes.
   - `MemoizeEntry{MemoizeKey *key; MemoizeTuple *tuplehead; uint32 hash;
     char status; bool complete;}` — 8+8+4+1+1 = 22, padded to the struct's
     own 8-byte pointer alignment = 24 bytes.
   - So `ExecEstimateCacheEntryOverheadBytes(n) = 48 + 16n`. Ported as
     `pgMemoizeEntryOverheadBytes` (`memoize_pgentrybytes.go`), with the three
     struct-size constants named individually so a future PG version bump can
     re-derive them from a diffed struct instead of re-deriving the sum.
3. **The `get_expr_width` sum over `param_exprs`** (the cache-key columns) has
   **no goopg counterpart to port to**: `get_expr_width` walks a `Node` to a
   per-column statistic (`clauses.c`), and goopg's cache keys are validated to
   be bare `*ColumnRef`s (`getMemoizePath`'s gate 7/8 commentary,
   `joinpathsmemoize.go:200-210`) but goopg has no per-expression average-width
   statistic wired to this site — the same gap M0139-0007's own
   `estEntryBytes` comment already named for the *tuple* width before this
   task (now fixed by term 1 above), except this time for the *key* width.
   **Per B2 rule 2, this is not absorbed** — it stays goopg's
   `hashsize.EntryBytes(nkeys, 0)` in BOTH currencies. Ledgered below with a
   follow-up task, not silently dropped.

This gives the substitution actually landed:

```
pgEstEntryBytes = pgRelationByteSize(tuples, pathWidth(innerPath)) +
                  pgMemoizeEntryOverheadBytes(tuples) +
                  hashsize.EntryBytes(nkeys, 0)   // unabsorbed key term, both currencies
```

gated by a new default-off switch, `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`, in the
exact shape of its two siblings R108 (`GOOPG_PG_HASH_TUPLE_SPILL_COST`) and
R113 (`GOOPG_PG_SORT_RELATION_BYTES_COST`) — this is now the **third** such
arm; AGENT.md's "known-stale claims" note caps the informal budget at "about
four... each one carries an explicit expiry (a measurement or a landed
consumer)". This task supplies that expiry in the same loop (see Decision
below), so the arm does not join the debt AGENT.md warns about.

## Code

- `internal/optimizer/memoize_pgentrybytes.go` (new): the switch
  (`pgMemoizeEntryBytesCost*`, mirroring `sort_pgrelationbytes.go`'s pattern)
  and `pgMemoizeEntryOverheadBytes`.
- `internal/optimizer/joinpathsmemoize.go`: `costMemoizeRescan` gained a
  `width int` parameter; when the switch is on AND `pgRelationByteSize`
  validates its inputs, the tuple-byte term uses the PG currency above,
  otherwise (switch off, or invalid width) it is byte-identical to the
  pre-task legacy formula. The `getMemoizePath` call site now passes
  `pathWidth(innerPath)`.
- `internal/optimizer/flaglabels.go`: `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`
  joined `flagResolvedState` and `flagProvenanceOrder`
  (`TestFlagProvenanceTableCoversPlannerEnv` enforces this for every new env
  var a benchmark artefact might need to name).
- `scripts/planner-flags.env`: regenerated via
  `go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env`
  (`TestFlagProvenanceEnvIsGenerated` enforces this stays in sync).

## Test — pinning the two currencies apart (B2's binding requirement)

`internal/optimizer/memoize_pgentrybytes_test.go`:

- `TestPGMemoizeEntryOverheadBytesStructSizes` pins the ported struct-size
  arithmetic (48 fixed, +16/tuple) so a future edit cannot silently drift the
  transcription away from the C struct sizes it represents.
- `TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy` — the switch defaults
  off and must reproduce the exact pre-task formula regardless of `width`,
  proving `width` cannot leak into the legacy currency.
- `TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols` — with the switch on, a
  wide PG pathtarget width must fit **fewer** cache entries into the same
  `workMem` than a narrow one even though `NCols` (goopg's own currency) is
  held constant, and the switch-off arm must show NO such sensitivity to
  width. This is the currency-separation pin B2 requires: the two currencies
  can disagree, and only the elected one does.

Existing tests: the three pre-existing `costMemoizeRescan` call sites
(`TestMemoizeWithoutStatisticsIsStrictlyMoreExpensive`,
`TestMemoizeWithStatisticsPricesTheHitRatio`,
`TestMemoizeNDistinctClampedToCalls`) updated for the new parameter, unchanged
otherwise — the switch defaults off so their assertions are untouched.

`go test ./internal/optimizer/...`: all packages pass (2.5s, cached where
unaffected).

## Measurement — adopt or hold (per M0139-0007a's own precedent: derive, then
## measure and decide in the same loop, never leave a new arm dangling)

Single binary (`tmp/goopg-m0139-0007b-bin`, deleted after use — never
`tmp/goopg-bench-bin`, the nightly lane's image). Never touched the shared
`:65432`/`:65433`/`:65437`/`:65438` clusters' data directories or their
running servers.

- **TPC-H**: `scripts/tpch-estimate-audit-arm.sh`, private port 5596,
  `PGSHAPED=1 PLAN_ONLY=1` (pins the current default explicitly, per the
  script's own warning that an unset flag stops being well-defined the day
  the default flips), arm captured with
  `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST=1` and a same-session control captured
  immediately after with the var unset (`NO_BUILD=1`, same binary — the env
  var is read once at package-var init). Artefacts:
  `analysis/leftdeep-joins/m0139-0007b-memoize-{off,on}.{txt,plans.txt,pg.plans.txt}`.
- **Same-session A/A control, not a stale-baseline diff.** The first attempt
  diffed the ON capture against the older, already-committed
  `analysis/m0141/m0141-s2a-fix1-tpch.plans.txt` baseline and found ~111
  modified lines — but every one was a Seq Scan / Hash Join row-count or cost
  drift with **no Memoize line among them**, and the two captures'
  `stats-epoch` stamps differed (`75dce25ff8088906` vs `a8ace61987e2484e`).
  That is the sampling-noise trap `goopg_plan_pin_three_drift_sources` and
  0007a's own "same-PG-reference control" note both warn about: a capture
  taken in a different server bring-up is not a valid diff target. Recaptured
  the OFF arm in the SAME loop, same binary, back-to-back with the ON capture,
  and diffed those two instead.
  - Result: **byte-identical except the header/label line.** TPC-H's single
    Memoize node (line 157 of both `.plans.txt`, an index-probe cache under a
    Nested Loop) prices at `cost=0.12..0.15 rows=1 width=490` in BOTH arms —
    the switch changes nothing here.
- **TPC-DS**: `scripts/lib/tpch-private-clone.sh`'s online
  `pg_basebackup -X fetch` snapshot of the SF0.25 goopg cluster
  (`bench/tpcds/runtime_goopg/data-sf025`, source port 65437) onto a private
  clone/port (5598), started via `scripts/goopg-test-run.sh` with
  `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` set/unset, captured with
  `scripts/capture-tpcds.sh` (db `postgres`, user `postgres`, matching
  `scripts/tpcds-sf025-regression.sh`'s own connection). Server stopped and
  the clone dir removed after each arm; the shared `:65437` cluster was never
  stopped/started/written. Artefacts:
  `analysis/m0139/m0139-0007b-memoize-{off,on}-tpcds-goopg.txt`.
  - Result: **byte-identical** apart from the header label line and the
    binary's PID/inode stamp (both arms ran the identical rebuilt binary).
    The corpus contains 13 Memoize nodes across its queries (`grep -c
    Memoize` = 39 counting header/body/cost lines); every one observed has
    `rows=1` (a single-row parameterised probe), e.g.
    `Memoize (cost=0.25..0.34 rows=1 width=420)`.

## Why byte-identical, not a bug in the substitution

The unit test proves the two currencies CAN disagree (`workMem=64KB`, wide
width, moderate tuple count). The corpora show they do not disagree *here*
because neither ever reaches the regime where the disagreement is visible:
`evictRatio = 1 - min(estCacheEntries, ndistinct)/ndistinct` is the only path
by which `estEntryBytes` reaches the final cost, and at both SF=1 (HammerDB
TPC-H, fresh capped server, `PLAN_ONLY` never runs `ANALYZE`) and SF=0.25
(TPC-DS's persisted stats), every observed Memoize candidate caches `rows=1`
per key against a `work_mem` pinned at 64 MB
(`scripts/capture-tpcds.sh`'s `PIN`) — `estCacheEntries` is enormous under
EITHER currency, so `min(estCacheEntries, ndistinct) == ndistinct` regardless
of which byte estimate produced `estCacheEntries`, `evictRatio` is 0 either
way, and the currency term never surfaces. This is the identical shape of
0007a's finding for R108/R113 (the corpora never reach the memory-constrained
regime the absorbed formula was written for) — not a coincidence, but the
expected consequence of measuring a spill/eviction-priced arm against
workloads that never spill.

## Decision: HOLD — stays default-off

Same reasoning as its two siblings, restated for this arm: zero category or
cost movement on either corpus is not evidence against the substitution (the
derivation is a direct, cited port, not tuned), it is evidence that TPC-H SF=1
and TPC-DS SF=0.25/SF=1 do not exercise Memoize's memory-constrained branch.
There is no plan-parity upside to weigh against promoting, and B2's own
caution about "planner charging below what the executor needs" (the direction
this arm's PG-vs-goopg gap runs, since goopg's real cache entry is Go-shaped
and larger than PG's C structs) means a promotion would still need the TPC-H
SF=1 execution acceptance-arm gate (`scripts/tpch-acceptance-arm.sh`) before
landing — not run here, because nothing in this measurement justifies running
it.

With this decision, **M0139-0007's three filed pieces (recon, 0007a, 0007b)
are all resolved** and the banner's "M0141-S2a-fix and M0139-0007" line's
`M0139-0007` half is complete; only M0141-S2a-fix's own remaining slices carry
that banner line forward.

## Deferral

The unabsorbed per-key width term (`get_expr_width` → goopg's
`hashsize.EntryBytes(nkeys, 0)`, both currencies) is recorded in
`.ralph/deferral_ledger.md` (task-id `m0139-0007b`) with a fix_plan follow-up
task, per the group's "a deferral needs TWO artefacts" rule.

## Verification

- `go build ./...`, `go vet ./internal/optimizer/...` clean.
- `go test ./internal/optimizer/...` — all pass (includes the three new tests
  above and the three updated pre-existing Memoize cost tests).
- Live measurement above; shared clusters read-only or online-cloned, never
  stopped/started, verified quiet before/after.
- Pre-commit pgbench smoke: see commit's own hook output (mandatory, not
  skippable per AGENT.md for this milestone group).

## No production diff to the default arm

`GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` defaults unset/off; every existing caller
of `costMemoizeRescan` and every existing test is byte-identical to
pre-task behaviour with the switch off (proven by
`TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy`).

## Resume points

- Port `get_expr_width`'s per-key-expression width statistic (deferred above)
  and fold it into both currencies' key-width term.
- Re-measure this arm if a Memoize-relevant workload with a large per-key
  cached row count (`tuples` >> 1) or a tight `work_mem` becomes part of the
  corpus — that is the regime `evictRatio` can actually move in, and the
  first place the two currencies would visibly disagree.

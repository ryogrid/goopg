# M0142-0003d — Q9's real divergence is a 2500x row-estimate collapse at
# `lineitem ⋈ partsupp`, not a cost-formula term (recon, 2026-09-16)

Status: closed (recon complete 2026-09-16 — premise refuted: the divergence
is a row-estimate collapse, not a cost-formula term).

*Filed by -0003c's Finding 2/3 as "which cost term underprices goopg's
index-driven NLI over `lineitem` vs a hash-join-with-full-scan". This recon
refutes that framing: the decisive divergence is upstream of costing
entirely — a catastrophic ROW-ESTIMATE under-count for the
`lineitem ⋈ partsupp` composite-key join, present from the smallest relset
that contains it. No production code changed this loop; measurement only.*

## 0. Method

Private capped scratch server (`GOOPG_CG_UNIT=m0142-0003d`,
`scripts/goopg-test-run.sh`, port 5533) on a throwaway copy of
`bench/tpch/runtime_goopg/data` (`cp -a` to `/tmp/m0142-0003d/data` — the live
TPC-H bench cluster at :65433 was never touched), built from HEAD with
`GOOPG_PGSHAPED_DP_TRACE=1`. Q9 (`tmp/c7-tpch-queries/q9.sql`, unmodified
schema-standard text) run via `EXPLAIN` twice (parallel default, then
`SET max_parallel_workers_per_gather=0`); DPPATH trace lines captured to
`server.log`. Real PG cross-checks ran read-only against the shared oracle
`:65432` (`tpch` db). Relid bitmap recovered from the leaf (`kind=0`) trace
lines and matches -0003a/R53's mapping exactly: `0=part 1=supplier 2=lineitem
3=partsupp 4=orders 5=nation`.

Evidence kept (tmp-only, referenced not committed —
consistent with -0003c's own tmp-evidence policy):
`/tmp/m0142-0003d/{server.log, q9-goopg.plan, q9-goopg-serial.plan,
q9-pg-goopg-actual-order.txt, q9-pg-goopg-actual-order-nohash.txt}`.

## 1. -0003c's "forced-goopg-order" was not actually goopg's order

-0003c forced real PG into a **FROM-clause literal left-deep chain**
(`join_collapse_limit=1` + explicit `((((part JOIN supplier) JOIN lineitem)
JOIN partsupp) JOIN orders) JOIN nation`, the six relations in the order they
appear in `q9.sql`'s FROM list) and read 336207.55 against PG's own default
204932.03, calling that "PG priced in goopg's own chosen order". It never
verified that shape against goopg's *actual* winning plan.

It is not. This loop's fresh, unforced `EXPLAIN` (both parallel and serial)
shows goopg's real Q9 winner is a **nested-loop index chain**: `part ⋈
partsupp` (hash), then `lineitem` via `lineitem_part_supp_fkidx`
(index-nested-loop), then `supplier` via `supplier_pk` (index-nested-loop),
then `orders` via `orders_pk` (index-nested-loop), then `nation` (hash, last).
It never does a full `lineitem` scan and never hash-joins `orders` in as a
base table — the opposite of what -0003c forced into PG. Confirmed against
the DPPATH trace: for `relids={0,1,2,3,5}` (the 5-way excluding orders), the
accepted winner is `producer=join.hash outer={0,1,2,3} inner={5}` (nation
folded in last by hash, cost 52714.14); for the full 6-way, the accepted
winner is `producer=nestloop.index outer={0,1,2,3,5} inner={4}` (orders
NL-indexed in last, cost 124537.12 serial / matches the serial `EXPLAIN`
top-node cost exactly).

Forcing real PG into *this* actual shape (same nesting, `join_collapse_limit
=1`/`from_collapse_limit=1`, hash allowed) costs only 129227.24 (parallel) —
barely worse than PG's own default (128538.93) — because PG's planner, even
shape-constrained, is still free to pick the join *algorithm* per node and
reverts `orders` back to a Hash Join (`Parallel Hash Join … Parallel Seq Scan
on orders`) instead of the forced NL. Disabling hash (`enable_hashjoin=off`,
serial) to force the *literal* algorithm goopg picked prices the same shape
at **372230.53** — essentially the same magnitude as -0003c's from-clause-order
number (336207.55), for a different reason. **Both of -0003c's and this
loop's "forced" PG numbers turn out to be measuring the same thing from two
different angles: PG's cost model strongly penalizes NL-indexing through
`orders` (1.5M rows) at that step. -0003c's Finding 2 (a 64% real-PG cost
gap between PG's own order and a forced order) survives, but the "goopg's
own chosen order" label on the forced query was wrong; §2 below is why it
doesn't matter — the real question is one level down.**

## 2. The decisive finding: row estimates collapse ~2500x, independent of any
## cost-formula constant

Tracing goopg's DP search's OWN row estimates (not costs) across relsets
containing `lineitem`(2) and `partsupp`(3) together:

| relids | relation set | rows (goopg, accepted, non-partial) | expected order of magnitude |
|---|---|---|---|
| `{2}` | lineitem alone | 6 001 265 | (base table) |
| `{3}` | partsupp alone | 800 000 | (base table) |
| `{0,3}` | part ⋈ partsupp | 48 484 | plausible (part filtered ~1-2%) |
| `{0,2}` | part ⋈ lineitem (single-col `l_partkey=p_partkey`) | 363 707 | plausible |
| **`{2,3}`** | **lineitem ⋈ partsupp (composite `l_partkey=ps_partkey AND l_suppkey=ps_suppkey`)** | **2 406** | **~5 999 098** (every lineitem row has exactly one matching partsupp row — see §3) |
| `{0,2,3}` | + part filter | 146 | should track `{2,3}`'s true value times part's ~1-2% selectivity, i.e. tens of thousands, not 146 |
| `{1,2,3}` | + supplier (no part) | 602–2406 (worker-scaled) | same order-of-magnitude problem, compounded |
| `{0,1,2,3}`…`{0,1,2,3,4,5}` | every relset that includes both 2 and 3 | **stays pinned at 146 all the way to the top** | — |

Every relset that includes lineitem+partsupp together inherits the same
~146-row floor (`{0,1,2,3}`, `{0,1,2,3,5}`, `{0,1,2,3,4}`,
`{0,1,2,3,4,5}` — all `rows=146`, confirmed identical to the query's own
FINAL output cardinality, 146 nation×year groups). Relsets that omit either
`2` or `3` do **not** collapse (`{0,1,2,4}=363671`, `{0,1,2,5}=363671`,
`{1,3}` alone ≈ 258065/800000, i.e. essentially unfiltered/near-cross —
expected, since supplier and partsupp share no direct predicate absent
lineitem). The trigger is specifically the pairing of relids `{2,3}`.

**This — not any hash/NL cost-formula constant, not `indexProbeCostMultiplier`
(B8), not the R53 spill-footprint mechanism — is what makes goopg's DP search
think an all-index-nested-loop chain through `orders` is nearly free: it
believes the entire join up to that point is only ~146 rows.** Every
downstream cost comparison in -0003a/-0003b/-0003c (`nestloop.index` pricing
"only 3-9 units above" `join.hash`) is comparing candidates built on this same
undercounted 146-row intermediate on BOTH sides — the near-tie those loops
measured is real, but it is a symptom of the wrong row estimate, not evidence
about the cost formula itself.

## 3. Why `{2,3}` should be ~6M, and why goopg's existing FK-substitution
## mechanism should already prevent this

`lineitem(l_partkey, l_suppkey)` is TPC-H's standard composite foreign key
into `partsupp(ps_partkey, ps_suppkey)` — every `lineitem` row has exactly one
matching `partsupp` row (verified live: `partsupp_pk` on the bench cluster
*is* `CREATE UNIQUE INDEX partsupp_pk … USING btree (ps_partkey, ps_suppkey)`,
`psql -h 127.0.0.1 -p 65433 -d tpch \d partsupp`). The true join cardinality
of `{2,3}` with no other filter applied is therefore ≈ `lineitem`'s own row
count, ~5 999 098 — not 2406 (a **~2494x** under-count).

goopg already has purpose-built code for exactly this pattern:
`internal/optimizer/joinkeyproof.go` (`superkeyJoinEstimate`,
`internal/optimizer/joinrelsize.go`'s `superkeyJoinSelectivity`,
`M0127-P5.6-f`) — its own header comment names this exact clause pair as the
motivating case: *"Q9 equates `l_suppkey = ps_suppkey AND l_partkey =
ps_partkey`. `estimateJoin` priced ONE of those pairs while
`joinResidualSelectivity` excluded BOTH… Only a declared FK may remove the
equality pairs and substitute `1/ref_tuples`."* — i.e. this mechanism was
written specifically to fix a Q9-shaped under/over-count regression, and its
own tests (`TestSuperkeyChickensOutOnPartialCover`,
`TestSuperkeyDoesNotComposeAcrossASelfJoin`, `TestCalcJoinrelSizeSuperkeySubsetLeavesResidual`)
show it is a live, exercised code path — not dead code
(`dead_code_is_not_a_reference_impl` does not apply here).

Despite that, the measured `{2,3}` estimate (2406) is nowhere near what the
superkey/FK substitution should produce (~6M, i.e. selectivity ≈ 1 on the
`lineitem`-driven direction, since `partsupp`'s side of the key is proven
unique by `partsupp_pk`). Two non-exclusive possibilities, **neither
confirmed by this recon — measurement stopped here deliberately**:

1. The mechanism doesn't reach the code path that actually computed the 2406
   estimate at HEAD (production `estimateJoin`/`cardinality.go` vs the
   PG-shaped DP `joinrelsize.go` arm — the joinkeyproof.go header itself warns
   these are two **separately implemented, sibling-paths-rule-bound** copies
   of the same algorithm; one could have regressed without the other).
2. The mechanism fires but its precondition-matching against this exact
   clause shape (`AND`-of-two-equalities, both column pairs, matched against
   `uniqueKeyColumnSets`'s composite-index list) fails to recognize the
   composite key for this specific relid pairing, order, or clause
   representation.

## 4. What this changes about the milestone's scope

- -0003c's Finding 2/3 (the 64% real-PG cost gap) and its causal story ("a
  real costing-term divergence… most likely centered on how goopg prices an
  index-driven Nested Loop against an already-shrunk composite relative to a
  hash join") is **superseded**: the composite isn't shrunk correctly in the
  first place, so nothing about the NL-vs-hash pricing of that composite is
  informative until the row estimate is fixed. -0003c's Finding 1
  (enumeration order is a faithful port — closed) is unaffected.
- The pre-M0138 R53 spill-footprint lead
  (`docs/design/not_ralph/plan_parity_fix_take2/r53-q9-costing-step0/SLICE1.md`)
  analyzed a *different* partition split (`{orders}|{5-way}`) under a stale
  row-estimate assumption from before this composite-key issue was isolated;
  it is not wrong on its own terms (a real spill-cost mechanism exists and
  the trace evidence for it is real) but it is very likely describing a
  second-order effect layered on top of a 2500x-wrong row estimate, not the
  primary driver. Do not resume it before §3 is resolved.
- `indexProbeCostMultiplier` (B8, still unsettled per M0142-0005's 2026-09-16
  recon) is **not implicated by this finding** — nothing here touches
  index-probe costing at all, only join row estimation.

## 5. Resume point — filed as M0142-0003e

Read `internal/optimizer/joinkeyproof.go`'s `superkeyJoinEstimate` +
`internal/optimizer/joinrelsize.go`'s `superkeyJoinSelectivity` (and its
production twin, `internal/optimizer/cardinality.go`'s `estimateJoin` +
whatever calls `joinResidualSelectivity`) side by side against this loop's
concrete repro: a 2-relation join of `lineitem` and `partsupp` on
`l_partkey = ps_partkey AND l_suppkey = ps_suppkey`, where `partsupp` has a
genuine composite UNIQUE index on exactly those two columns
(`partsupp_pk`). Determine, with a targeted unit test in
`internal/optimizer/joinrelsize_test.go` or `cardinality_test.go` (not a
full-query trace), whether `superkeyJoinSelectivity`/`estimateJoin` reaches
this exact case and if not, why (which arm is live by default —
`GOOPG_PGSHAPED_DP`'s current default value needs re-confirming, not
assumed, per `goopg_arm_scripts_disable_dp_search` in memory). Do NOT assume
which of §3's two possibilities is correct — the unit test settles it.

## 6. Gates

`git status --porcelain -- internal/` empty before and after (measurement
only, no production code touched). Scratch server stopped
(`/tmp/m0142-0003d/goopg stop -D /tmp/m0142-0003d/data`), port 5533 confirmed
free. Shared TPC-H bench cluster (`:65433`) never restarted or written to —
only a single read-only `\d partsupp` / `pg_indexes` query issued against it.
Real PG oracle (`:65432`) used read-only. `go build ./...` clean (no source
edited). Practice-card row-count gate suite not required (no production code
touched, same reasoning as -0003c).

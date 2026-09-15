# M0142-0015 — Q45 recon: is the `item`/`customer_address` DP-search flip a M0142-0012 formula defect?

Status: accepted (landed 2026-09-16, recon only, no production code changed)

## Task

Per `.ralph/fix_plan.md`'s M0142-0015 line (filed by M0142-0014's triage):
M0142-0012 flipped TPC-DS Q45's join order for `item`/`customer_address` away
from real PG's own choice (pre-fix goopg matched PG exactly; post-fix it
swaps the last two joins). Determine which of the two orderings actually
prices cheaper under goopg's cost model, and whether that price is *more* or
*less* accurate than the alternative — i.e. is this a genuine M0142-0012
edge case worth tightening, or the same DP-search near-tie/enumeration-order
class M0142-0003b/0003c already opened for Q9's level-6?

Measurement-only per `AGENT.md` §"Plan-parity harness" — this recon changed
no production code (a temporary `GOOPG_PGSHAPED_DP_TRACE=1` env var was used,
not a code edit).

## Method

Built `/tmp/goopg-m0142-0015/goopg` (`go build ./cmd/goopg`, plain HEAD, no
diff). Cloned `bench/tpcds/runtime_goopg/data-sf025` (804M) to a private
throwaway dir (`/tmp/pp-m0142-0015/data`, deleted after use) rather than
touching the shared `:65437` cluster — same precedent as M0142-0003a/0010/
0011/0012a. Capped scratch server (`GOOPG_CG_UNIT=m0142-0015`,
`scripts/goopg-test-run.sh`) on port `5534`, `GOOPG_PGSHAPED_DP_TRACE=1`.
Table data lives in the `postgres` database, not a `tpcds025` database
(`pg_on_goopg_catalog_lacks_pg_stat_views`-adjacent: goopg's DB catalog is
in-memory-only on a plain `start`, table data persists under whichever DB
name it was loaded into, which for this SF0.25 dir is `postgres`).
`SET max_parallel_workers_per_gather = 0;` then plain `EXPLAIN` on Q45's
exact SQL (`bench/tpcds/runtime_goopg/tpcds-data/queries/query45.sql`).

For the PG side, `bench/tpcds/server.sh status` showed all three TPC-DS
lanes (`sf1`/`sf025`/`pg`) down at recon start (the concurrent nightly batch
was still in its TPC-H stage), so `bench/tpcds/server.sh start pg` /
`stop pg` bracketed a single read-only EXPLAIN session against the shared
`:65438` reference (role `ryo`, db `tpcds025`) — safe because it is a
read-only reference server with a documented lifecycle script and did not
collide with the nightly batch's own stage ordering. Ran Q45 twice: once
unmodified (default planner order) and once with `join_collapse_limit = 1`
+ `from_collapse_limit = 1` and the four join predicates rewritten as
explicit `JOIN ... ON` clauses in the left-deep order `web_sales, date_dim,
customer, item, customer_address` (i.e. forcing PG to try goopg's post-fix
order) to read PG's own cost for the alternative ordering, which `EXPLAIN`
never shows for a path it didn't choose. Evidence:
`analysis/m0142/m0142-0015-q45-{goopg-explain,dptrace,pg-default-and-forced}.txt`.

## Finding 1 — goopg's own DP search treats the two orderings as cost-equal to ~13 significant digits

`DPPATH` at level 5 (the full 5-relation relset) shows **two** `nestloop.index`
candidates both marked `verdict=accepted` (neither dominated) for the two
orderings in question:

| candidate (outer+inner) | meaning | total cost |
|---|---|---|
| `{web_sales,customer,customer_address,date_dim}` + `{item}` | PG's order (`ca` before `item`) | `9674.299956003799` |
| `{web_sales,customer,date_dim,item}` + `{customer_address}` | goopg's chosen order (`item` before `ca`) | `9674.299956003797` |

The difference is `2e-12` — floating-point noise, not a real cost
distinction. Unlike Q9's level-6 finding (M0142-0003b), this is **not** an
exact bit-for-bit tie caught by `addToPathlist`'s dominance check (which
would mark the later arrival `dominated`) — both survive as `accepted`
because they are not *exactly* equal, and `setCheapest` then takes the
literal numeric minimum. The PG-matching candidate
(`{...,customer_address}` before `item`) is registered **first**
(`DPTRACE pair … lev=5 created=1` for that partition; the goopg-order
partition's own pairing shows `created=0`, i.e. it reaches the same relset
second) — so registration order does **not** decide this one either;
goopg's chosen order wins purely because its literal float total lands
`2e-12` lower, an artifact of which arithmetic path the two candidates'
marginal-cost sums took to the same value, not a meaningful cost claim.

This traces back one level: the level-4 legs feeding these two level-5
candidates are **not** tied — `{customer+customer_address+date_dim+web_sales}`
costs `9606.167256844004` and `{customer+date_dim+item+web_sales}` costs
`9596.427936003798`, a real (if small, ~0.1%) 9.74 difference favoring the
item-first leg. The level-5 step then adds each remaining relation's index
probe, and those marginal costs happen to compensate almost exactly in the
opposite direction, producing the near-perfect final tie. Two real,
non-tied per-level cost differences summing to a near-perfect final tie is
not evidence of a formula bug in either term individually.

## Finding 2 — real PG 18.3 exhibits the identical near-tie on the same query

Forcing PG to try the alternative order (`join_collapse_limit=1`, explicit
left-deep `JOIN`) produces a plan whose **top-level total cost is identical
to 2 decimal places** to PG's own default-order plan: `9827.36` both ways.
The two orderings' *inner* Nested Loop costs genuinely differ (`9700.77`
default vs `9695.48` forced, a real 5.29 difference — the PG-side mirror of
goopg's level-4 gap above), but the marginal cost of probing the last
relation (`item` in the default order, `customer_address` in the forced
order) compensates in the opposite direction, landing both chains at the
same displayed total. PG's `EXPLAIN` only prints 2 decimal digits so a
`2e-12`-scale distinction (goopg's own margin) is invisible at this
resolution, but the qualitative shape — two real, unequal per-step costs
canceling into a near-perfect whole-plan tie — reproduces exactly.

## Verdict

**This is the same class of finding as M0142-0003b/0003c's Q9 level-6 tie,
not a new M0142-0012 formula defect.** Both PG and goopg cost the two Q45
join orderings as effectively indistinguishable; which one each engine
picks is decided by sub-ULP-scale floating-point behavior (goopg) or an
analogous PG-internal tie-break neither this recon nor M0142-0003c has
fully attributed yet (PG). M0142-0012's own formula is not implicated —
tightening it cannot move a margin this small in any principled direction,
and there is no "more accurate" answer to converge on since the two
orderings' true costs are, for practical purposes, equal in *both* engines'
own models. The task line's own fork ("M0142-0012's new formula has an edge
case worth tightening" vs "the real defect is elsewhere") resolves to
neither literally — the defect, if any, is the cross-engine tie-break
mechanism itself, already tracked by M0142-0003c (filed for Q9, now with a
second corroborating witness). **No code change follows from this recon.**
Per the practice card's K50 warning, no DP-search reordering fix is
attempted here — that remains M0142-0003c's scope, and it should widen to
use Q45 as a second, cross-corpus witness rather than a Q9-only question
when it is next picked up.

## Gates

Measurement-only task; no production code touched
(`git diff -- internal/ 2>/dev/null` empty for this loop). No gate rerun
required beyond the recon's own instrumentation runs above.

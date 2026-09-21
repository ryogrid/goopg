# M0145-0019 — nested-loop costing for a derived inner: the hypothesis is REFUTED

Status: **RECON COMPLETE 2026-09-22. The task's working hypothesis is refuted
by measurement, and the divergence it was looking for is in a different
place.** No production file touched. The fix is filed as **M0145-0019a**.
Kind: recon
Parent: M0145-0018
Movement: none

## What the task asked, and what it assumed

M0145-0018's NO-GO ended with three owner options, and the banner's GO chose
**(c) — "fix the nested-loop pricing for a derived inner"**, on the reasoning
that *"the 1.1M estimate losing to nothing suggests the comparison, not the
estimate, is where the fault sits"*. The task text names the hypothesis and
says it is **to be tested, not assumed**: that PG plans this shape without a
categorical guard because its NL pricing rejects the election on the merits.

Tested. It is wrong in an informative way: goopg's NL pricing is **not** the
problem, and neither is candidate generation.

## Step 1 — reproduce, then instrument

SF1 TPC-DS, offline `cp -a` clone of `bench/tpcds/runtime_goopg/data` on a
private `:5561` lane under the cgroup cap, knob arm
(`GOOPG_JOINTREE_PIPELINE=1`), `GOOPG_DERIVED_FIREWALL=off`, EXPLAIN-only.
Q78's election reproduces exactly as 0018 recorded it:

```
->  Nested Loop Left Join  (cost=5494.86..1147565.07 rows=5731 width=240)
      Join Filter: (((cs_sold_year = ss_sold_year) AND (cs_item_sk = ...)) AND (cs_customer_sk = ...))
      ->  Hash Left Join  (cost=5494.86..26516.81 rows=10317 width=160)
      ->  CTE Scan on cs  (cost=0.00..10868.30 rows=5380 width=80)
```

`GOOPG_PGSHAPED_DP_TRACE=1` then answers the question the EXPLAIN cannot —
what was offered at that joinrel, and what was decided:

```
DPPATH path producer=join.hash     relids={0,1,2} rows=10317 startup=16457.31 total=37659.80  verdict=accepted jointype=left outer={0,1} inner={2}
DPPATH path producer=join.nestloop relids={0,1,2} rows=10317 startup=5494.86  total=1147507.76 verdict=accepted jointype=left outer={0,1} inner={2}
```

**Both candidates are generated, and both are accepted.** The hash join is
~30× cheaper on total cost and it is sitting in the pathlist. So:

- the NL is *not* mispriced — 1.1M is charged, and charged correctly;
- the hash path is *not* missing — the "losing to nothing" reading of 0018 is
  not what happens;
- `add_path` keeping both is *correct*: the NL has the lower STARTUP cost
  (5494.86 vs 16457.31), and a lower-startup path is not dominated by a
  cheaper-total one in PG either.

Instrumenting `addPath` rather than the cost functions is what made this
visible; the cost numbers alone look like a pricing story.

## The actual divergence: the fraction is applied to the WRONG REL

Q78 ends in `ORDER BY … LIMIT 100`. goopg resolves the LIMIT fraction at the
**join search root** — `searchCtx.finalPath` (`internal/optimizer/joinsearch.go:317`)
calls `getCheapestFractionalPath(finalRel, tupleFraction)`. With
`tupleFraction = 100` converted against the rel's 10317 rows (f = 0.009693):

| candidate | startup | total | fractional = s + f·(t−s) |
|---|---|---|---|
| `join.hash` | 16457.31 | 37659.80 | **16662.82** |
| `join.nestloop` | 5494.86 | 1147507.76 | **16564.09** |

The nested loop wins by **98.7, or 0.6%** — and is then fed to a `Sort`, which
must consume every row. The 0.6% fractional "saving" becomes a 30× cost, which
is the 60×-plus SF1 regression 0018 caught.

**PostgreSQL cannot make this election, and not because of pricing.** Its
fractional selection happens at the FINAL upper rel, after the Sort is priced:

- `planner.c:439` — `best_path = get_cheapest_fractional_path(final_rel, tuple_fraction)`,
  where `final_rel` is `fetch_upper_rel(root, UPPERREL_FINAL, NULL)`, i.e. the
  top of the upper-rel lattice, not the join rel.
- `planner.c:5314` — `create_ordered_paths` takes
  `cheapest_input_path = input_rel->cheapest_total_path`.
- `planner.c:7646`+ — `make_ordered_path` returns NULL for any unsorted input
  path that is not the cheapest-total one (absent incremental sort).

So upstream sorts the cheapest-**total** path, and only then asks which of the
*sorted* paths wins at the fraction. Once the Sort is priced its startup cost
contains the whole input's total, so a low-startup NL has nothing left to win
with.

`finalPath`'s own doc comment cites `planner.c:437` for exactly this call. The
**citation is right and the rel is wrong**: goopg applies upstream's final-rel
rule at the join-search root, which is the one place the Sort does not exist
yet.

## Why this closes out both of 0018's remaining options

- **(c) fix NL pricing for a derived inner** — there is nothing to fix. The
  price is right; a change here would be tuning toward a plan (R6).
- **(b) narrow the firewall to an NL-inner-derived veto** — would suppress this
  symptom while leaving the rule that produced it. Any query whose LIMIT
  fraction is resolved before a mandatory Sort can elect a low-startup path the
  same way, with no derived input anywhere in it.

The faithful fix is a **third option the task did not list**: move the
fractional selection to the final upper rel, so it sees the Sort. That is
`create_ordered_paths` + `get_cheapest_fractional_path` placement, i.e. a
structural alignment this milestone is already doing for other upper rels
(M0145-0006 landed upper-rel pathlists).

## Blast radius — why the implementation is filed separately, not bundled

This is not a Q78 fix. Every query with `ORDER BY … LIMIT` over a join whose
pathlist holds a low-startup/high-total path can change plan, in either
direction, and the current placement has been live since M0127-P5.9
(2026-08-06). That needs the full value-gate set and a plan-parity capture of
its own, which is why M0145-0019's deliverable is this document and the
follow-on task rather than a patch.

## Evidence

- `tmp/m0145-0019-q78-firewall-off-plan.txt` — the reproduced SF1 plan.
- `tmp/m0145-0019-q78-dppath-final-joinrel.txt` — the `DPPATH` records for the
  final joinrel, both arms of the election.

## Deliverable

**M0145-0019a — apply the LIMIT fraction at the final upper rel, not the join
search root.** Filed in `.ralph/fix_plan.md`. On its landing, re-run
M0145-0018's fresh E1 at SF0.25 and SF1 against the then-current
`outer-over-derived` fire set, per M0145-0019's closing instruction.

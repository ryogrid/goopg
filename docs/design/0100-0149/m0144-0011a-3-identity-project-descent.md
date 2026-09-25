# M0144-0011a-3 — crossing a positional-identity `Project` at the ORDERED seam

Status: LANDED 2026-09-20
Kind: impl
Parent: M0144-0011a
Milestone: M0144 (plan-parity harness, measurement-first era)
Predecessor: `docs/design/0100-0149/m0144-0011a-ordered-step-ordering-claim.md`

## 1. Why this task exists, and why it is not the task that was filed

M0144-0011a landed the `*Aggregate` arm of `inputNodePathkeys` and moved 16
TPC-DS SF0.25 plans. It named three queries it did NOT move — Q21, Q62, Q99 —
and filed **M0144-0011a-2** on the assumption that they were blocked by
`electOrderedGrouping`'s `len(cands) < 2` gate or by the walk's remaining
`default: nil` tops.

That assumption was wrong, and measuring it first is what this loop bought.
A `GOOPG_PGSHAPED_DP_TRACE=1` probe on a private `:5595` SF0.25 lane
(`tmp/c20a/data-sf025`) says:

- **Q62 and Q99** plan a `Finalize HashAggregate`. A hashed aggregate emits no
  order at all, so `aggregateEmissionPathkeys` declines correctly and no
  ordering-claim work can ever move them. Their divergence is
  `aggregation-strategy` (PG picks GroupAggregate), a different mechanism
  entirely. They are removed from this lineage's expected movement.
- **Q21** declines with `DPGROUP loop-decline reason=gate-precondition` — not
  `cands<2(1)` — and then its ORDERED seed arrives with `keys=0`. A trace on
  the walk's `default:` arm names the stop node exactly:

```
DPWALK stop-at=*optimizer.Project out=4 nodeout=4 agree=true
DPWALK project child=*optimizer.Aggregate childout=4 isolated=false agreechild=false
DPWALK   target[0]=ColumnRef{Index:0,Name:"w_warehouse_name"} outname="w_warehouse_name"
DPWALK   target[1]=ColumnRef{Index:1,Name:"i_item_id"}        outname="i_item_id"
DPWALK   target[2]=ColumnRef{Index:2,Name:"sum"}              outname="inv_before"
DPWALK   target[3]=ColumnRef{Index:3,Name:"sum"}              outname="inv_after"
```

Every target is the child's column at the same index. The Project's entire job
is to **rename** the two aggregate outputs to `inv_before` / `inv_after` — and
that rename is why `schemaCoordinatesAgree` reports `agreechild=false`, which
is what kept the `*Aggregate` arm from ever being consulted.

So the residue is a `*Project`, and the two bullets M0144-0011a-2 actually
states are still open and still unmeasured. This task is filed for the
mechanism the probe found; M0144-0011a-2 stays `[ ]` with its expected movement
corrected.

## 2. The change

`inputNodePathkeys`' walk gains a third descent, narrower than the other two:

```go
case *Project:
        if !projectIsPositionalIdentity(t) {
                return nil
        }
        renamed = true
        n = t.Child
```

`projectIsPositionalIdentity` admits a Project only on positive evidence that
it re-assigns nothing: target `j` is `ColumnRef{Index: j}` for every `j`, over
an equally wide child, and the node is not `IsolatedScope`. Such a Project can
only rename; it cannot move, drop, duplicate or compute a column.

### Why the general PG rule does not port, and the identity case does

PG states the rule outright and needs no special case:

```c
/* postgres/src/backend/optimizer/util/pathnode.c:2936-2937 */
/* Projection does not change the sort order */
pathnode->path.pathkeys = subpath->pathkeys;
```

It can say that for ANY projection because a PG pathkey names an
`EquivalenceClass`, which is immune to output-position changes. goopg's
pathkeys name POSITIONS, so the general rule is unsound here — and the previous
comment in `upperorderedinput.go` refused every Project for exactly that
reason. Under the identity restriction, "PG's position" and "goopg's position"
coincide by construction, and the rule ports with no translation at all.

### The `renamed` flag

Once an identity Project has been crossed, the schema the seam PUBLISHES may
differ from the terminal node's own in LABELS while provably not differing in
POSITIONS. So the walk's per-step check weakens from `schemaCoordinatesAgree`
to a column-count test (`agrees`), and the delivered claim is restamped from
the published schema at the SAME index (`relabelPathkeysTo`).

`relabelPathkeysTo` is a relabel, not a mapping — no index changes hands. It is
cosmetic today (`exprEqual` compares a `*ColumnRef` on `Index` alone,
`exprwalk.go:668`, so `pathkeysContainedIn` would agree either way); it exists
so the published claim is spelled in the vocabulary the seam publishes
(Q21's `inv_before`, not the aggregate's `sum`). It truncates on the first
unusable key, the same rule `validatedSearchPathkeys` obeys.

### Deliberately refused, each with its own reason

| refused | reason |
|---|---|
| `IsolatedScope` | its targets' `ColumnRef`s are inner-indexed then outer-relabelled, so `Index == j` is not reading the coordinate it appears to read |
| narrowing (`len(out) < len(child)`) | positions `0..len(out)-1` would still be an identity and the claim would still be sound, but the walk's agreement check is a column-count test and would need a second "prefix" mode — filed, not guessed (ledger row) |
| permutation (`Index != j`) | sound only with a position MAP, which is the translation this file's header rules out |
| any non-`*ColumnRef` target | a computed column is a new value; an ordering claim on the input says nothing about it |
| no stated targets | absence of evidence is not evidence of identity (the pre-existing test case, kept) |

## 3. Result

Q21 on the private lane, before and after:

```
 Limit                                        Limit
   ->  Sort                          ==>        ->  GroupAggregate
         Sort Key: w_warehouse_name, i_item_id        Group Key: w_warehouse_name, i_item_id
         ->  GroupAggregate                           Filter: (CASE WHEN inv_before > 0 …)
               Group Key: …                           ->  Sort
               Filter: (CASE WHEN …)                        Sort Key: w_warehouse_name, i_item_id
               ->  Sort …                                   ->  Gather …
```

**First-divergence census** (`scripts/pg-plan-first-divergence.py`) — this is
where the result is visible:

```
before:  Q21 depth=1 [sort-strategy]   under Limit: PG GroupAggregate | goopg Sort
after:   Q21 depth=1 [qual-placement]  under Limit: PG GroupAggregate | goopg GroupAggregate
```

The node kind at depth 1 now MATCHES PG. The first divergence advanced past the
ordering seam and the residue is the HAVING qual's placement — the exact
"advance past depth-1 where the seed ordering was the sole blocker" shape the
0011a lineage was filed to produce.

## 4. Measurement (AGENT.md D2), and why it is `Movement: none`

### 4.1 TPC-DS SF0.25

Same lane, same dataset, consecutive `scripts/tpcds-sf025-regression.sh sweep`
captures; PG reference `analysis/m0142/m0142-0012verify-tpcds-pg.txt`.

before (`plans-20260920-144915.txt`):

```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=24 rendering=26
```

after (`plans-20260920-151739.txt`):

```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=25 rendering=26
```

`sort-strategy` does NOT move. The reason is worth recording, because it is a
property of the instrument rather than of the change: `CATEGORIES-EXCL-MATCH`
counts, per query, every category in which that query diverges ANYWHERE — and
Q21's inner `Sort` still differs in strategy from PG's plan, so Q21 keeps its
`sort-strategy` tally while ALSO gaining `qual-placement` (+1). The
first-divergence census in §3 is the instrument that does see the advance, and
it is not one of S3's three.

### 4.2 TPC-H SF1, parallel canonical mode

`estimate-audit -plan-only -serial=false`, `GOOPG_ANALYZE_SEED=20260905`, stats
epoch `e4a554b2a4cfb710` — the same epoch as 0011a's pair, so the arms are
directly comparable.

```
PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES-EXCL-MATCH: join-order=16 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
```

Identical to the 0011a arm — and the capture's plans file is **byte-identical**
to `analysis/m0144/m0144-0011a-on-parallel.plans.txt`
(`sha256 1727126e8ed714565be36461a80d0f0be18777f722a9eede2be4edf65393208d`).
Per G3 that equality needs proof it is not the same binary measured twice: the
serving binaries differ,
`7372bbf60b170386e1dcc6b44de5bba3853313fdbe6390610ef0558351254703` here versus
`163ba219e018a91a5a3cd3f3bc4fc7ad5ddb34b374aeab05a24c696e183900b4` for 0011a.
Two different binaries producing the same 22 plans is the honest reading: no
TPC-H query reaches the ORDERED step through a renaming Project. The duplicate
capture file is therefore not committed; the sha above is the record.

### 4.3 shape-delta

`queries=99 same=98 changed=1 added=0 removed=0` — changed: Q21 only. A
one-query blast radius is what the identity restriction is for.

### 4.4 stats epoch

TPC-H `e4a554b2a4cfb710` (pinned, matches 0011a's pair). TPC-DS: consecutive
same-day sweeps on the gate-owned `:65437` dataset, no reload between them,
98/99 byte-identical plans.

### 4.5 planning route

PG-shaped search (`GOOPG_PGSHAPED_DP=1`, default). The change is in the
upper-rel pipeline above the search seam.

### 4.6 seam-decline census

`N/A — the change adds a descent at the ORDERED seam; it files no path and
declines no join-search seam.`

### 4.7 wall time of every query whose plan changed

Q21 is the only one. The sweep's status-delta channel: `verdict-changes=none
runtime-moves=1 total-delta=-2.3%` — the total moved DOWN and no ledger row for
a slower plan is owed. (Q21 is the `runtime-moves=1` entry and it got faster:
one fewer sort of the grouped output.)

### 4.8 Movement

`Movement: none` — S3's three instruments are the match count,
`CATEGORIES-EXCL-MATCH` beyond ±3, and `ea-ratchet`; none of them moved. The
first-divergence advance in §3 is real work and a real result, but it is not
movement under the rule, and calling it movement would be exactly the kind of
instrument-shopping S3 exists to prevent.

## 5. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` (PASS=96, SKIP=3) |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES vs `tmp/arm-m0144-0011a.txt` |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2, TPC-DS SF0.25 match 2, both floors held |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched` |

## 6. Still open after this

- **M0144-0011a-2** keeps its two original bullets, both still unmeasured:
  `electOrderedGrouping`'s `cands<2` gate versus `planner.c:5337`, and the
  `*MergeJoin` / `*GatherMerge` / `*IncrementalSort` walk tops. Its expected
  movement is corrected there: Q21 is spent, and Q62/Q99 were never its
  business.
- The **narrowing** Project (`len(out) < len(child)`) is sound in principle and
  refused here only because the walk's agreement check has no "prefix" mode.
  Ledger row filed with the resume point.
- Q62/Q99's `Finalize HashAggregate` is an `aggregation-strategy` divergence
  and belongs to that lineage, not this one.

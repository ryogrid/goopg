# M0146-0014 — census at 607ec0402 (2026-11-01): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base tree 1061393dbc62, reason=plan-relevant-change)
with triggering paths `internal/postmaster/dispatch.go` + `internal/catalog/pubsub.go` — the refresh
data-half commit `607ec0402` (M0122-0014 AlterSubscription_refresh data half) touches dispatch.go at
the same two executor-context build sites (the 4th slot-hook wiring
`ectx.RefreshSubscriptionOnPublisher = s.subscriptionSlotHooks()`), which carries no GUC/planner
setting, and pubsub.go's new `DropSubscriptionRel` registry method. The change's runtime surface
(publisher fetch dial, pg_subscription_rel rows, tablesync COPY, apply-worker restart, walsender exit
arms, slot confirmed_flush advance) is elsewhere. tree= below: `git diff --quiet -- internal cmd
scripts bench go.mod go.sum` held while the capture's candidate binaries were built (the code commit
was in HEAD; only `.ralph/` bookkeeping was dirty). This is an unchanged census under S9(4a), so only
this file is committed; the captures are deleted from `tmp/census-20261101c/` after this record.
tree=7a76494f06835bc3d7c97182dae97bf97a52392e; capture repo head 607ec0402
(the code commit; the recording commit is this file + bookkeeping on top).

Capture: `BASELINE_REV=12aef8583 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101c`
(BASELINE_REV = the routing-20261101 base tree's commit, so the plan A/B spans the whole window since
that census; fires=none on all three corpora, PLAN-PARITY 22/22 + 99/99 + 99/99 vs PG, and the A/B's
own baseline-vs-candidate match was 22/22 + 99/99 + 99/99 with divergent=0).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261101-compare` (exit 0; the three candidate captures
renamed to `cap-<corpus>.plans.txt`):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3), candidate arm, per corpus:

- tpcds-sf025 + tpcds-sf1: tmp/fireset-bin/s9f-candidate sha256
  a9ae8f00b2c6e8dfdfcd0cfb59a0a366c4066b091d1d56e4bfc829a2ab51611f (built from HEAD's tree at capture start).
- tpch: tmp/fireset-bin/s9f-tpch-runner sha256
  fa286d145f520893bda1a08afe565bed474e49d3c7ca661ac5a172d38c2804a7.

Both differ from routing-20261101's 96fe3271022d0e3d1770be376cf6082f5dbef90b8a49f2820a149c437af251f2 /
089c1730329b85426f1206c152db857a348f803b3f202f1c74a6fe2abeb9b9f0 — a new binary was measured (G3), and
the plan text is byte-identical to the base on all three corpora (the same three plan-text hashes the
routing-20261101 record reports). The change is subscription replication plumbing (publisher table
fetch, pg_subscription_rel row swap, tablesync COPY, apply-worker kill/relaunch, walsender exit arms,
slot confirmed_flush exclusive-boundary fix) — nothing on the EXPLAIN plan-text path.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261101 note (which names the routing-20261031d note).

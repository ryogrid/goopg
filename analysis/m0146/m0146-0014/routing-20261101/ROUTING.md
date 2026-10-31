# M0146-0014 — census at 12aef8583 (2026-11-01): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base tree 1a5b27b2a888, reason=plan-relevant-change)
with triggering path `internal/postmaster/dispatch.go` — the only trigger-set file `12aef8583`
(ALTER SUBSCRIPTION publication-list refresh half, M0122-0014) touched; it enters the set as the
GUC→planner-settings surface, and this loop's diff to it is the third executor-context hook wiring
(`ectx.CheckPublicationsOnPublisher = s.subscriptionSlotHooks()`) at the two context-build sites, which
carries no GUC/planner setting. The change's runtime surface (replication dial + check_publications
SELECT, executor arm, testport pin) is elsewhere. tree= below: `git diff --quiet -- internal cmd scripts
bench go.mod go.sum` held while the capture's candidate binary was built (the code commit was in HEAD;
only `.ralph/` bookkeeping was dirty). This is an unchanged census under S9(4a), so only this file is
committed; the captures were deleted from `tmp/census-20261031e/`.
tree=1061393dbc62c485514d4c279dc7934f9a54b6da; capture repo head 12aef8583
(the recording commit on top of 12aef8583, `analysis/` only).

Capture: `CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9e tmp/census-20261031e`
(BASELINE_REV=HEAD=12aef8583, so the plan A/B compares the committed tree against itself; fires=none on
all three corpora, PLAN-PARITY 22/22 + 99/99 + 99/99 vs PG).

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

- tpcds-sf025 + tpcds-sf1: tmp/fireset-bin/s9e-candidate sha256
  96fe3271022d0e3d1770be376cf6082f5dbef90b8a49f2820a149c437af251f2 (built from HEAD's tree at capture start).
- tpch: tmp/fireset-bin/s9e-tpch-runner sha256
  089c1730329b85426f1206c152db857a348f803b3f202f1c74a6fe2abeb9b9f0.

Both differ from the routing-20261031d capture's 08263882d6d2b3c2154291e9cf00ee2d4452f6c313f014c2e43764b541be503f /
b1c29bccc95ed56cdd4d61478082675700ac89ac1947125b724ba4a0329cad0e — a new binary was measured (G3), and the plan
text is byte-identical to the base on all three corpora (the same three plan-text hashes the routing-20261031d
record reports). The change is subscription replication plumbing (publisher dial, pg_publication existence
SELECT, executor arm, hook wiring) — nothing on the EXPLAIN plan-text path; the fireset A/B independently
measured fires=none on all three corpora.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261031d note (which names the routing-20261031c note).

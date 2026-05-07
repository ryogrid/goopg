# PostgreSQL Oracle Compatibility Report (M0060)

Generated at: 2026-05-07T00:05:34+09:00

## Inventory Snapshot

| suite_id | kind | discovered_cases |
| -------- | ---- | ---------------: |
| client-tools-tap | tap | 89 |
| contrib-suites | mixed | 57 |
| isolation-specs | isolation | 121 |
| modules-suites | mixed | 44 |
| recovery-tap | tap | 47 |
| regress-expected | regress | 265 |
| regress-sql | regress | 232 |
| subscription-tap | tap | 36 |

## Status Summary

| suite_type | port | defer | excluded |
| ---------- | ----:| -----:| --------:|
| isolation | 0 | 1 | 0 |
| mixed | 0 | 2 | 0 |
| modules | 0 | 0 | 1 |
| regress | 0 | 1 | 0 |
| tap | 10 | 3 | 1 |

## Deferred Blockers

| id | upstream_path | suite_type | deferred_to | rationale |
|----|---------------|------------|-------------|-----------|
| D-001 | `postgres/src/test/regress` | regress | `M0060-0002` | Requires dedicated pg_regress-compatible runner and normalization policy while upstream SQL files remain migration targets. |
| D-002 | `postgres/src/test/isolation` | isolation | `M0060-0004` | Requires deterministic multi-session scheduler and expected-schedule comparator integration. |
| D-003 | `postgres/src/test/recovery` | tap | `M0060-0005` | Recovery TAP scenarios require replication/failover capability growth before pass-required promotion. |
| D-004 | `postgres/src/test/subscription` | tap | `M0060-0005` | Subscription TAP scenarios require logical replication capability growth before pass-required promotion. |
| D-005 | `postgres/src/bin/scripts/t` | tap | `M0060-0003` | Client utility script suites are in scope and tracked but require broader SQL/catalog parity before pass-required promotion. |
| D-006 | `postgres/src/test/modules` | mixed | `M0060-0005` | Modules migration is staged by dependency class and extension assumptions. |
| D-007 | `postgres/contrib` | mixed | `M0060-0005` | Contrib migration is staged by dependency class and extension/runtime assumptions. |

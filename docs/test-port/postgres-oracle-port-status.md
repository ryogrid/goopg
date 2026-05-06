# PostgreSQL Oracle Test-Port Status

This file tracks migration-target and non-passing visibility for M0060.

Status meanings:

- `port`: migrated and pass-required.
- `defer`: in scope, not yet pass-required.
- `excluded`: explicitly out of scope by M0060 exclusion policy.

## In-Scope But Not Yet Pass-Required (`defer`)

| id | upstream_path | suite_type | status | pass_required | rationale | deferred_to |
|----|---------------|------------|--------|---------------|-----------|-------------|
| D-001 | `postgres/src/test/regress` | regress | defer | no | Requires dedicated pg_regress-compatible runner and output normalization. | `M0060-0002` |
| D-002 | `postgres/src/test/isolation` | isolation | defer | no | Requires deterministic multi-session scheduler and expected-schedule comparator. | `M0060-0004` |
| D-003 | `postgres/src/test/recovery` | tap | defer | no | Recovery TAP scenarios require staged harness expansion and feature enablement. | `M0060-0005` |
| D-004 | `postgres/src/test/subscription` | tap | defer | no | Subscription/logical replication scenarios need staged porting and capability growth. | `M0060-0005` |
| D-005 | `postgres/src/bin/*/t` | tap | defer | no | Client-tool TAP suites are migration targets under M0060 and must be reclassified from legacy skip. | `M0060-0003` |
| D-006 | `postgres/src/test/modules/*` | mixed | defer | no | Requires per-module dependency classification before migration. | `M0060-0005` |
| D-007 | `postgres/contrib/*/{sql,expected,t}` | mixed | defer | no | Contrib suites require phased migration by dependency class. | `M0060-0005` |

## Explicitly Excluded (`excluded`)

| id | upstream_path | suite_type | status | pass_required | rationale | deferred_to |
|----|---------------|------------|--------|---------------|-----------|-------------|
| E-001 | `postgres/src/test/modules/unsafe_tests` | modules | excluded | no | Explicit unsafe test set is outside goopg compatibility target by policy. | `-` |

## Notes

- Do not keep non-passing migration targets undocumented.
- Each `defer` row must map to a concrete milestone task.
- Add rows at finer granularity as migration proceeds (directory -> file -> case).
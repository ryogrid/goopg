# Executable Isolation Tests for READ COMMITTED Semantics

This document lists upstream isolation specs that explicitly validate `READ COMMITTED` behavior (or compare it against other isolation levels).

Selection policy:
- Include specs that explicitly use `BEGIN ISOLATION LEVEL READ COMMITTED`, set `default_transaction_isolation` to `read committed`, or document RC-specific behavior in comments.
- Focus on specs suitable for staged execution porting and parity checks.

## Candidate Specs

| spec | why it is RC-relevant |
| ---- | --------------------- |
| `postgres/src/test/isolation/specs/eval-plan-qual.spec` | Header explicitly states EvalPlanQual is used in READ COMMITTED isolation level. |
| `postgres/src/test/isolation/specs/eval-plan-qual-trigger.spec` | Runs RC and RR paths side-by-side for trigger-related EPQ behavior. |
| `postgres/src/test/isolation/specs/lock-committed-keyupdate.spec` | Header says failures are expected except in READ COMMITTED mode. |
| `postgres/src/test/isolation/specs/lock-committed-update.spec` | Explicit RC/RR/SERIALIZABLE comparison for committed-update locking behavior. |
| `postgres/src/test/isolation/specs/insert-conflict-do-update.spec` | Comment says behavior is permitted only in READ COMMITTED mode. |
| `postgres/src/test/isolation/specs/insert-conflict-do-update-2.spec` | Sessions are explicitly started with READ COMMITTED to validate ON CONFLICT behavior. |
| `postgres/src/test/isolation/specs/insert-conflict-do-update-3.spec` | Header discusses user-visible MVCC effects specific to READ COMMITTED mode. |
| `postgres/src/test/isolation/specs/insert-conflict-do-update-4.spec` | Comment explicitly says scenario works only in READ COMMITTED mode. |
| `postgres/src/test/isolation/specs/insert-conflict-do-nothing.spec` | Both sessions run with READ COMMITTED for conflict handling checks. |
| `postgres/src/test/isolation/specs/insert-conflict-specconflict.spec` | Uses `SET default_transaction_isolation = 'read committed'` in controller/sessions. |
| `postgres/src/test/isolation/specs/drop-index-concurrently-1.spec` | Header documents expected result difference at READ COMMITTED vs stricter levels. |
| `postgres/src/test/isolation/specs/fk-snapshot.spec` | Explicit RC vs RR permutations for FK snapshot behavior. |
| `postgres/src/test/isolation/specs/partition-key-update-1.spec` | Contains explicit READ COMMITTED transaction paths for partition key updates. |
| `postgres/src/test/isolation/specs/partition-key-update-2.spec` | Uses READ COMMITTED setup steps in all sessions. |
| `postgres/src/test/isolation/specs/partition-key-update-3.spec` | Uses READ COMMITTED setup for concurrent partition-key update scenarios. |
| `postgres/src/test/isolation/specs/partition-key-update-4.spec` | Includes explicit READ COMMITTED begin steps in concurrency permutations. |
| `postgres/src/test/isolation/specs/merge-update.spec` | Uses READ COMMITTED begin blocks while validating MERGE update conflicts. |
| `postgres/src/test/isolation/specs/merge-delete.spec` | Uses READ COMMITTED begin blocks while validating MERGE delete conflicts. |
| `postgres/src/test/isolation/specs/merge-insert-update.spec` | Uses READ COMMITTED begin blocks for MERGE insert/update conflict behavior. |
| `postgres/src/test/isolation/specs/merge-match-recheck.spec` | Uses READ COMMITTED begin blocks for recheck semantics in MERGE matching. |
| `postgres/src/test/isolation/specs/merge-join.spec` | Uses READ COMMITTED begin blocks in MERGE + join scheduling checks. |

## Notes

- This list is intentionally scoped to specs with explicit RC evidence.
- Additional isolation specs may still be relevant indirectly, but are excluded here unless RC intent is explicit.
- For execution planning, this set can be split into:
  - Core RC correctness (EPQ, lock-committed, ON CONFLICT)
  - RC under partitioning/MERGE/FK interactions

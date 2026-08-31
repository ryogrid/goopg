# M0134-0161 — `pg_index.indimmediate` is keyed on DEFERRABLE, not INITIALLY DEFERRED

**Status:** landed 2026-08-29
**Regress case:** `postgres/src/test/regress/sql/replica_identity.sql`
(`not-tried` → `failed`, 194 → 189 diff lines, `^-ERROR` 3 → 2)
**Scope:** engine-wide (catalog fidelity + two independent validation paths).
This is not a replica-identity fix; `replica_identity.sql` is only the first
regress case that exercises the divergence.

## The bug

`pg_index.indimmediate` means "the uniqueness check is enforced immediately on
insertion". PostgreSQL derives it from the `DEFERRABLE` flag **alone**:

```c
/* postgres/src/backend/catalog/index.c:1045-1051 — index_create() */
UpdateIndexRelation(..., (constr_flags & INDEX_CONSTR_CREATE_DEFERRABLE) == 0, ...);

/* postgres/src/backend/catalog/index.c:2080-2082 — index_set_state_flags() */
if (deferrable && indexForm->indimmediate)
    indexForm->indimmediate = false;
```

`INITIALLY {IMMEDIATE|DEFERRED}` never enters either expression. A constraint
declared merely `DEFERRABLE` — i.e. `DEFERRABLE INITIALLY IMMEDIATE`, the
default — is therefore **non-immediate**, because a later `SET CONSTRAINTS` can
defer it even though it is not deferred by default.

goopg had **four sites that disagreed** about this:

| site | before | correct? |
|---|---|---|
| `internal/executor/deferred_unique.go` `uniqueCheckDeferred` | `idx.Deferrable` | ✅ (and cites index.c:2080-2082) |
| `internal/catalog/catalog.go` pg_index virtual row builder | hardcoded `"t"` | ❌ |
| `internal/executor/pg18_user_catalog_rows.go` pg_index heap row builder | hardcoded `true` | ❌ |
| `internal/executor/operators_ddl.go` `resolveReplicaIdentityIndex` | `idx.InitiallyDeferred` | ❌ |
| `internal/parser/analyzer/analyzer.go` ON CONFLICT arbiter | `idx.InitiallyDeferred` | ❌ |
| `internal/optimizer/planner.go` `resolveArbiterIndex` (ON CONSTRAINT branch) | `idx.InitiallyDeferred` | ❌ |
| `internal/optimizer/planner.go` `resolveArbiterIndex` (inferred-by-column branch) | *no check at all* | ❌ |

The two wrong validation paths **silently accepted** indexes PostgreSQL
rejects. That is the class of defect this milestone keeps finding: a green
result produced by not performing a check, not by performing it correctly.

## Oracle verification

Probed on a throwaway PG 18.3 (`initdb` to `/tmp`, port 15499) before changing
anything, because two of the affected sites had existing goopg tests pinning
the old behaviour:

```
CREATE TABLE d1 (a int primary key, b int not null, c int not null,
  CONSTRAINT u_defer      UNIQUE (b) DEFERRABLE,
  CONSTRAINT u_initdefer  UNIQUE (c) DEFERRABLE INITIALLY DEFERRED);

     idx     | indimmediate
-------------+--------------
 d1_pkey     | t
 u_defer     | f      <-- merely DEFERRABLE is NON-immediate
 u_initdefer | f

INSERT … ON CONFLICT ON CONSTRAINT u_defer DO NOTHING;
  ERROR:  ON CONFLICT does not support deferrable unique constraints/exclusion constraints as arbiters
INSERT … ON CONFLICT (b) DO NOTHING;            -- inferred-by-column form
  ERROR:  ON CONFLICT does not support deferrable unique constraints/exclusion constraints as arbiters
ALTER TABLE d1 REPLICA IDENTITY USING INDEX u_defer;
  ERROR:  cannot use non-immediate index "u_defer" as replica identity
```

The existing tests turned out to set **both** `Deferrable` and
`InitiallyDeferred`, so they assert a true statement and needed no correction —
only their doc comments' parenthetical "(INITIALLY DEFERRED)" was misleading
about *why*.

## The fix

One documented predicate, `(*catalog.Index).IsImmediate() == !i.Deferrable`
(`internal/catalog/catalog.go`), carrying the upstream citation and the
drift history. Every consumer now routes through it, and the `Deferrable` /
`InitiallyDeferred` field comments forbid reading them directly for this
question.

### Ordering constraint in the inferred-by-column arbiter path

The new check in `resolveArbiterIndex`'s inference branch must run **after** the
index has been matched against the inference specification, not as a `continue`
filter inside the matching loop. PostgreSQL's `infer_arbiter_indexes`
deliberately does not filter on `indimmediate`:

> Let executor complain about !indimmediate case directly, because the index
> may be used by an INSERT with a different ON CONFLICT clause.
> — `postgres/src/backend/optimizer/util/plancat.c:817`

`ExecCheckIndexConstraints` raises instead (`execIndexing.c:604-610`). Skipping
the index during matching would surface `42P10 "there is no unique or exclusion
constraint matching the ON CONFLICT specification"` where PG reports `55000`.

## Verification

13-case regress A/B against a HEAD worktree (`/tmp/goopg-indimm-base`):
`replica_identity` 194 → 189, **twelve byte-identical**, zero regressions.
`create_index`'s only delta is the pre-existing nondeterministic Go pointer
address in `pg_get_indexdef` (`&{105 0x… C}`), unrelated to this change.

Cases swept: `replica_identity insert_conflict alter_table create_index
constraints indexing create_table index_including publication updatable_views
triggers with identity`.

Regression tests added:

- `internal/optimizer/with_test.go` `TestResolveArbiterIndexRejectsMerelyDeferrableArbiter`
  — both arbiter branches (ON CONSTRAINT and inferred-by-column).
- `internal/executor/replica_identity_indimmediate_test.go`
  `TestReplicaIdentityRejectsMerelyDeferrableIndex` (rejection **and** the
  NOT-DEFERRABLE sibling still accepted) and
  `TestPgIndexIndimmediateTracksDeferrable` (catalog view agrees with the
  validation path — the drift that hid the bug).

## Deferred — `replica_identity.sql` remains `failed`

Filed as M0134-0161a…h; see `.ralph/deferral_ledger.md` for resume points. The
remaining 189 diff lines are eight independent causes, none of them
indimmediate-related:

1. `'pg_constraint'::regclass` resolves to no `pg_class` row (returns 0 rows).
2. A foreign table's index gives `index "…" does not exist` where PG gives
   `"…" is not an index for table "…"` (42809) — goopg only searches indexes on
   the target table, PG does a namespace-wide relname lookup then checks
   `indrelid`.
3. An inline `id serial constraint pk primary key deferrable` does not name the
   backing index `pk`.
4. `pg_get_expr` schema-qualifies a column default unconditionally:
   `nextval('public.x_id_seq'::regclass)` vs PG's search_path-aware
   `nextval('x_id_seq'::regclass)`. Same class as m0134-0032; also visible in
   `alter_table`, `dependency`, `publication`.
5. Partial-index predicate deparse: `WHERE (keyb <> '3')` vs
   `WHERE keyb <> '3'::text`.
6. `ALTER COLUMN … TYPE bigint` is not reflected in `\d`.
7. `CREATE TABLE … PARTITION OF` leaves `Number of partitions: 0`, and a
   partitioned PK index is not marked `INVALID` before `ATTACH PARTITION`.
8. A spurious `LINE 1: … ^` position pointer on `DROP CONSTRAINT` not-found.

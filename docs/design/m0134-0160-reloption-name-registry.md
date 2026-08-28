# M0134-0160 — reloption name/namespace registry

**Status:** landed 2026-08-29 · **Task:** M0134-0160 (`reloptions.sql`) ·
**Upstream oracle:** PG 18.3 `postgres/src/backend/access/common/reloptions.c`

## The gap this closes

goopg validated storage parameters only by *recognising* them. Every
`WITH (...)` consumer was a chain of

```go
if v, ok := s.With["fillfactor"]; ok { /* parse, bounds-check, store */ }
```

so a name that no block looked for was **silently accepted and dropped**:

```sql
CREATE TABLE t(i int) WITH (not_existing_option=2);        -- goopg: CREATE TABLE
CREATE TABLE t(i int) WITH (bad_ns.fillfactor=2);          -- goopg: CREATE TABLE
CREATE INDEX i ON t (c) WITH (not_existing_option=2);      -- goopg: CREATE INDEX
ALTER TABLE t SET (no_such_option=1);                      -- goopg: ALTER TABLE
```

PG rejects all four with `ERRCODE_INVALID_PARAMETER_VALUE` (22023). This is a
correctness gap on its own — a typo'd `autovacuum_enable=false` looks like it
took effect and does nothing — and it *cascades* in test corpora that reuse one
relation name across negative cases: the first silently-accepted option creates
the relation, so every later statement reports a spurious
`relation "x" already exists` instead of its own error. `execCreateIndex`'s
`buffering` enum check (M0134-0127) already documents that exact cascade for a
single option; this change closes it for every name.

## Upstream model

PG keeps five static tables — `boolRelOpts` / `intRelOpts` / `realRelOpts` /
`enumRelOpts` / `stringRelOpts` — each entry tagging a name with a bitmask of
the relation kinds that admit it (`relopt_kind`,
`postgres/src/include/access/reloptions.h:39-56`). Two separate checks run over
a `WITH` clause, in this order:

| pass | function | error | citation |
|---|---|---|---|
| 1 — namespaces | `transformRelOptions` | `unrecognized parameter namespace "%s"` | `reloptions.c:1275` |
| 2 — names | `parseRelOptions` | `unrecognized parameter "%s"` | `reloptions.c:1488` |

`transformRelOptions` runs to completion over the whole `DefElem` list before
`parseRelOptions` sees anything, so a clause carrying **both** faults reports
the namespace one.

Callers differ in exactly three parameters, and all three are load-bearing:

| caller | kind | `validnsps` | `acceptOidsOff` |
|---|---|---|---|
| `DefineRelation` (`tablecmds.c:933`) | HEAP (or TOAST for the `toast.` namespace) | `HEAP_RELOPT_NAMESPACES` = `{"toast"}` | **true** |
| CTAS (`createas.c:124`) | HEAP | `{"toast"}` | **true** |
| `ATExecSetRelOptions` (`tablecmds.c:16690`) | relkind-dispatched: HEAP / VIEW / index AM | `{"toast"}` | false |
| `DefineIndex` (`indexcmds.c:911`) | the AM's (`amoptions`) | **NULL** — any namespace is an error | false |

`acceptOidsOff` is the legacy `WITH (oids = ...)` filter
(`reloptions.c:1307-1322`): an *unqualified* `oids` option is not a reloption at
all — `oids = true` raises "tables declared WITH OIDS are not supported",
`oids = false` is silently skipped. Missing this is a real regression source:
`create_table.sql` exercises `CREATE TEMP TABLE withoutoid() WITH (oids =
false)` and a first cut of this change broke it.

## What landed

New `internal/executor/reloptions_catalog.go`:

- `relOptKind` — the `relopt_kind` bitmask, restricted to the kinds goopg can be
  asked about through SQL (HEAP, TOAST, BTREE, HASH, GIST, SPGIST, GIN, BRIN,
  VIEW). ATTRIBUTE and TABLESPACE are deliberately absent: `ALTER TABLE … ALTER
  COLUMN SET (n_distinct…)` and `ALTER TABLESPACE … SET (…)` have their own
  validation paths and never reach a `WITH` map.
- `relOptionKinds` — the union of the five upstream tables, flattened to
  name → kind bitmask. The HEAP set is 24 names and the TOAST set 18, which is
  *exactly* what `execCreateTable` already extracts; the registry adds no
  option, it only records which names exist.
- `relOptionNamespaces` — `{"toast": relOptToast}`. Options landing in it are
  validated against RELOPT_KIND_TOAST because `DefineRelation` hands them to
  `heap_reloptions(RELKIND_TOASTVALUE, …)`.
- `indexRelOptKind(method)` — AM name → kind. An empty method is btree
  (`gram.y` defaults `access_method_clause` to `DEFAULT_INDEX_TYPE`); an unknown
  AM yields 0, admitting nothing, the same outcome as an `amoptions == NULL` AM.
- `validateRelOptionNames` / `validateRelOptionMap` — the two-pass check.

Wired at five sites in `internal/executor/operators_ddl.go`: `execCreateTable`,
`execCreateTableAs`, `execCreatePartitionChild` (all HEAP + `toast` namespace +
`acceptOidsOff`), `execCreateIndex` (AM kind, no namespace), `AlterIndexSetReloptions`
(AM kind, no namespace) and `execAlterTableSetReloptions` (HEAP or VIEW by
relkind, `toast` namespace, no `acceptOidsOff`). It **subsumes** the previous
mixed-case-only guard (`if k != strings.ToLower(k)` → 42000): a double-quoted
`"Fillfactor"` simply misses the registry, and now reports PG's 22023 rather
than goopg's 42000.

### Ordering matters at CREATE INDEX

`DefineIndex` calls `index_reloptions()` well before `index_create()` reaches
the name-conflict test, and that is observable — `reloptions.sql` reuses one
index name across its negative cases and expects `unrecognized parameter "x"`,
not "relation already exists". The validation is therefore placed **before**
the duplicate-name check in `execCreateIndex`, and `TestCreateRelOptionsRejectedEndToEnd`
pins it.

### Parser change

`CREATE INDEX`'s `WITH` clause reached the AST as seven typed fields
(`Fillfactor`, `DeduplicateItems`, …) with every other name discarded in
`indexOptsFrom` — so the executor could not tell "absent" from "unrecognized".
`CreateIndexStmt.WithOptionNames []string` now carries every name verbatim in
source order. `parity_goldens.txt` was regenerated: **37 goldens changed and all
37 differ only by the added field** (36 `∅`, one `["fillfactor"]`), i.e. purely
additive, no pinned AST moved.

## Two existing tests pinned non-PG behavior

Both were verified against a live PG 18.3 oracle before being changed:

- `TestCreateIndexBufferingEnumValidation` used `USING btree … WITH (buffering
  = …)`. `buffering` is RELOPT_KIND_GIST only, so real PG raises `unrecognized
  parameter "buffering"` before any enum check runs. Switched to `USING gist`,
  where the enum check the test is about is actually reachable.
- `TestAlterIndexSetTablespaceUpdatesIndex` asserted `ALTER INDEX <btree> SET
  (fastupdate = off)` succeeds. `fastupdate` is RELOPT_KIND_GIN; PG raises
  `unrecognized parameter "fastupdate"`. The arm now runs on a GIN index and
  additionally asserts the btree rejection.
- `TestAlterTableSetReloptionsBounds` explicitly pinned "an unrecognized
  lowercase option is accepted and ignored" — the bug itself. Now pins the
  22023 rejection.

## Verification

14-case regress A/B against a HEAD worktree (`/tmp/goopg-relopt-base`):

| case | before | after |
|---|---|---|
| `reloptions` | 232 | **201** |
| `alter_table` | 3792 | **3784** |
| other 12 | — | byte-identical |

`reloptions` `^+ERROR` 17 → 6 and `^-ERROR` 18 → 7. `alter_table`'s −8 is two
previously-missing `unrecognized parameter "autovacuum_enabled"` errors goopg
now raises, with no new error class. `create_index`'s only textual delta is a
pre-existing nondeterministic Go pointer address in a `pg_get_indexdef` output
(`&{105 0x… C}`), unrelated to this change.

Gates: `go test ./internal/executor/` PASS · `go test ./internal/parser/` PASS ·
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS ·
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34) · pgbench smoke via
the commit hook.

## Deferred (see `.ralph/deferral_ledger.md`, 2026-08-29 M0134-0160)

`reloptions.sql` remains `failed` at 201 lines. The remaining causes are
independent of this one:

1. **Duplicate option not detected.** `WITH (fillfactor=30, fillfactor=40)` must
   raise `parameter "fillfactor" specified more than once`
   (`reloptions.c:1230`). `CreateTableStmt.With` is a `map`, so the second
   binding silently wins. This is also the source cascade for the last three
   `+ERROR`s in the case. Fix shape: mirror this change's `WithOptionNames` onto
   `CreateTableStmt` / `AlterTableAction`, which also removes the
   lexicographic-vs-source-order caveat in `validateRelOptionNames`.
2. **`ALTER TABLE/INDEX … SET` applies only four options.**
   `execAlterTableSetReloptions` handles `parallel_workers`, `fillfactor`,
   `autovacuum_enabled`, `toast_tuple_target` (plus the view trio); everything
   else — including the whole `toast.` namespace and index `fillfactor` — is
   validated then ignored.
3. **`RESET (x = v)` is accepted**; PG raises `RESET must not include values for
   parameters` (`reloptions.c:1240`). `RESET (toast.x)` is a parse error.
4. **Numeric error-message rendering.** PG echoes the input string verbatim
   (`value -10.0 out of bounds`); goopg reformats the parsed float (`value -10`).
   `fillfactor=-30.1` likewise reports "invalid value for integer option" where
   PG reports the bounds error.
5. **A bare non-boolean option name is dropped.** `WITH (fillfactor)` must be
   read as `fillfactor=true` and then fail the integer parse
   (`reloptions.c:1291-1296`).

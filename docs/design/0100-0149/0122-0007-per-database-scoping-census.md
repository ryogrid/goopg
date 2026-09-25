# M0122\-0007 per\-database scoping census

Status: **measurement only, 2026\-09\-22** (two passes: loops \#24 and \#25). No code change. Parent: M0122\-0007
(per\-database\-namespace epic, slices 4b\-4e open).

## Why this exists

Four consecutive loops each found a per\-database scoping defect while working
on something else (pg\_dump porting, foreign\-table durability). That pattern
suggested a family rather than isolated bugs, and an open owner decision —
Option A vs Option B under the `[!]` template1 item in M0119\-0006 — turns on
how large that family actually is. Option A routes around the `DefaultDBOid`
sentinel for template1; Option B gives every database its own oid at every
layer. Choosing between them needs the scope measured, not estimated.

This document is that measurement. It does **not** choose between the options.

## Method

One goopg cluster, `initdb` defaults. Objects created in the `postgres`
database, then read back from `template1` and from a `CREATE DATABASE`\-created
`userdb`. PostgreSQL 18.3 is the oracle: in PG none of these objects is
visible from another database, because each database has its own catalog.

## Result

| probe (object created in `postgres`) | `template1` | `userdb` | PG 18.3 |
|---|---|---|---|
| `SELECT count(*) FROM p_tab` | **1 — leaks** | `42P01` ✓ | `42P01` |
| `SELECT nextval('p_seq')` | **1 — leaks** | `42P01` ✓ | `42P01` |
| `SELECT * FROM p_view` | **1 — leaks** | `42P01` ✓ | `42P01` |
| `SELECT p_fn()` | **42 — leaks** | **42 — leaks** | `42883` |
| index `p_idx` in `pg_class` | **1 — leaks** | `0` ✓ | `0` |

Two independent defects, with different blast radii:

### 1. `template1` shares `postgres`' namespace — everything leaks

Every object type tested is visible from `template1`. This is the known
`DefaultDBOid` sentinel: `ResolveDatabaseOid` returns `DefaultDBOid` for
template1, which is the same namespace `postgres` folds onto via
`catalog.NamespaceDBOid`. `userdb` is correctly isolated for four of the five
probes, which is what localises the fault to the shared sentinel rather than to
database scoping in general.

Consequence beyond visibility, measured separately: `pg_dumpall` emits
`postgres`' user tables twice — once under `\connect template1`, once under
`\connect postgres` — so restoring a goopg cluster dump into real PostgreSQL
plants user tables inside `template1`, which every database created afterwards
then inherits.

### 2. Functions are not database\-scoped — and `pg_proc` is global

This one is **not** the template1 sentinel, and Option A would not fix it.

- `p_fn()`, created in `postgres`, is callable from `userdb` (returns 42).
- `u_fn()`, created in `userdb`, is **not** callable from `postgres`
  (`function u_fn does not exist`).
- `pg_proc` in **every** database lists **both** `p_fn` and `u_fn`.

The asymmetry is the tell: function resolution falls back to the default
namespace, so objects created there are reachable everywhere while objects
created in a real per\-database namespace are not. `pg_class` does **not**
behave this way — `p_tab` is 1 row in `postgres` and 0 in `userdb` — so the
catalog layer is already scoped for relations and is not for procedures.

In PostgreSQL `pg_proc` is a per\-database catalog; both facts are divergences.

## Second pass (2026\-09\-22, loop \#25): is defect 2 a one\-off or a class?

The first pass left that open, and it changes Option B's size, so it was
measured. Objects created in `postgres` **after** `userdb` already existed (so
template inheritance cannot explain any result), probed from `userdb`:

| catalog | object created in `postgres` | seen from `userdb` | PG 18.3 |
|---|---|---|---|
| `pg_class` | table `p_t` | `0` ✓ | `0` |
| `pg_type` | domain / enum / composite | `0` ✓ | `0` |
| `pg_trigger` | trigger `p_trg` | `0` ✓ | `0` |
| `pg_proc` | function `p_trgfn` | **1 ✗** | `0` |
| `pg_namespace` | schema `p_schema` | **1 ✗** | `0` |

**Answer: a class of exactly two — `pg_proc` and `pg_namespace`.** Relations,
types and triggers are correctly scoped, so this is not "catalogs are global";
it is two specific registries.

The schema case is **worse than the function case**, in two ways:

- It is **usable, not merely visible**: `CREATE TABLE p_schema.inuserdb(a int)`
  succeeds from `userdb` against a schema created in `postgres`.
- It is **bidirectional**: a schema created in `userdb` is equally visible from
  `postgres`. The function defect is asymmetric (only the default namespace
  leaks outward), which is what made it look like a fallback; this one is a
  genuinely global registry.

Note for anyone reading the M0119\-0006 history: the 2026\-09\-22 `bw` slice
landed a **per\-database `pg_namespace` reload**, which fixed restart
durability of schemas. That is a different axis from live cross\-database
scoping, which is what this row measures — `bw` is not contradicted by it.

## What this says about the pending decision

Stated as evidence, not as a recommendation:

- Option A (stop returning `DefaultDBOid` for template1) addresses defect 1
  completely and defect 2 **not at all**.
- Option B (one oid per database at every layer) addresses both by
  construction, and is the larger change.
- Defect 2 is a class of **two** registries (`pg_proc`, `pg_namespace`), not a
  one\-off, and not a general catalog failure — three other catalogs are
  correctly scoped. That bounds Option B's extra surface rather than leaving it
  open\-ended.
- The census does not establish that defect 2 is cheap or expensive to fix
  independently; no attempt was made to locate its fix site, because that
  would be scope this measurement does not need.

## Known family members not re\-measured here

Cited rather than re\-derived, to keep the census honest about what was
actually run this loop:

- `COPY` resolves relations against the default database, so `pg_dump` of any
  user\-created database emits schema but no data (M0122\-0015a). Fails loudly:
  `pg_dumpall` exits 1.
- A template1 extension re\-attributes to `postgres` on restart (the `[!]` item
  this census serves).
- The four foreign\-data catalogs are pinned to `DefaultDBOid` (ledgered
  2026\-09\-22 under M0122\-0015).
- `ANALYZE <table>` inside a non\-default database errors "relation does not
  exist" (`CLAUDE.md`, the bench\-reorg ANALYZE\-scope note).

## Gates

None — no code changed. Every probe is reproducible from the two tables above
against a throwaway cluster.

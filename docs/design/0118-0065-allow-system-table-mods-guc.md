# 0118-0065 — `allow_system_table_mods` GUC (M0118-0008 `reindex-concurrently-toast` enabler)

**Status:** accepted
**Type:** enabler, **NOT** a promotion
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency isolation specs)

## Problem

`reindex-concurrently-toast.spec` is one of the remaining deferred specs in the
M0118-0008 group. Its **global setup** does:

```sql
SET allow_system_table_mods TO true;
DO $$DECLARE r record;
  BEGIN
  SELECT INTO r reltoastrelid::regclass::text AS table_name FROM pg_class
    WHERE oid = 'reind_con_wide'::regclass;
  EXECUTE 'ALTER TABLE ' || r.table_name || ' RENAME TO reind_con_toast;';
  ...
END$$;
```

The spec deliberately abuses `allow_system_table_mods` so it can rename a table's
non-deterministically-named TOAST relation / TOAST index to fixed names and then
`REINDEX … CONCURRENTLY pg_toast.reind_con_toast` them (REINDEX CONCURRENTLY
cannot run inside a transaction, so the names must be deterministic).

goopg did **not** register the `allow_system_table_mods` GUC, so the very first
setup statement failed:

```
pq: unrecognized configuration parameter "allow_system_table_mods" (22023)
```

— aborting permutation 0 before any session step ran.

## Change

Register `allow_system_table_mods` as a recognised boolean GUC, mirroring PG's
`guc_tables.c` entry exactly:

| attribute | value | source |
|-----------|-------|--------|
| name      | `allow_system_table_mods` | `guc_tables.c:1926` |
| context   | `PGC_SUSET` → goopg `ContextSuset` | superuser-settable via `SET` |
| category  | `DEVELOPER_OPTIONS` | (goopg has no category field) |
| type      | bool | |
| boot value| `off` | |
| flags     | `GUC_NOT_IN_SAMPLE` | (see sample-file note) |

This follows the established precedent of `allow_in_place_tablespaces`
(0118-—, M0095-0003): another `PGC_SUSET` `DEVELOPER_OPTIONS` boolean that goopg
recognises but treats as a no-op.

Files:
- `internal/config/defaults.go` — register the variable after
  `allow_in_place_tablespaces`.
- `internal/config/postgresql.conf.sample` — add the commented `#allow_system_table_mods = off`
  entry. goopg's `TestSampleConfigCoversRegistry` invariant requires **every**
  file-settable (non-`FlagDisallowInFile`) GUC to appear in the sample with its
  boot value; PGC_SUSET GUCs are file-settable, so the entry is mandatory for the
  parity gate even though PG marks the GUC `GUC_NOT_IN_SAMPLE` (the same
  divergence already exists for `allow_in_place_tablespaces`).
- `internal/config/allow_system_table_mods_test.go` — unit test (registered,
  boot `off`, `ContextSuset`, `TypeBool`, settable per session).

**goopg does NOT gate any catalog-structure modification on this GUC.** It is a
recognised no-op so that test/regression setups which `SET allow_system_table_mods
= on` succeed instead of erroring. This is correct for goopg's threat model: the
real protection (refusing structural ALTER/REINDEX on a system catalog) is a
separate concern not exercised by any currently-`port` spec.

## Why this does NOT promote `reindex-concurrently-toast`

After this enabler the setup divergence advances from the GUC error to the next
blocker, but the spec remains fundamentally **multi-loop, Effort-L**:

1. **(now)** the `DO` block hits
   `qualified names are not supported in PL/pgSQL expressions in v0 (0A000)` —
   `r.table_name` referenced inside an `EXECUTE … || r.table_name || …`
   expression. The PL/pgSQL `r.field` substitution added by 0118-0054 covers
   `SELECT … INTO` embedded SQL but not a general expression operand.
2. **(fundamental)** even with that fixed, the whole spec hinges on
   `pg_class.reltoastrelid::regclass` resolving to a **real TOAST relation** that
   can be renamed (`ALTER TABLE pg_toast.<n> RENAME …`) and reindexed
   (`REINDEX … CONCURRENTLY pg_toast.<name>`). goopg stores `text`/`bytea`
   **inline** (no out-of-line TOAST storage — see the `plpgsql-toast` design
   0118-0054 note), so it has **no TOAST relations** as catalog objects and
   `reltoastrelid` is `0`. Modeling TOAST relations as first-class catalog
   objects (own `pg_class` rows, OIDs, indexes, reindexability) is a distinct
   subsystem, well beyond one loop.

So `reindex-concurrently-toast` stays `defer`; this loop removes one concrete
wart (an incorrectly-rejected valid PG GUC) and records the remaining blocker
chain in the deferral ledger.

## Blast radius

Nil for normal operation: a brand-new recognised GUC, default `off`, wired to no
behavior. Useful beyond this spec — any PG regression/isolation/upgrade script
that toggles `allow_system_table_mods` during setup now runs instead of erroring.

## Gates

- `go test -run 'TestAllowSystemTableModsGUC' ./internal/config/` PASS.
- Full `go test ./internal/config/...` PASS (including
  `TestSampleConfigCoversRegistry` after the sample entry).
- `go build ./...` clean.
- Live probe: `reindex-concurrently-toast` global-setup divergence advanced from
  `unrecognized configuration parameter "allow_system_table_mods"` to the
  PL/pgSQL qualified-name error (next blocker).
- pgbench TPC-B smoke = pre-commit hook.

# 0118-0036 — inherit-temp: per-session temp-relation ownership (foundation)

Milestone: **M0118-0008** (DDL / VACUUM / maintenance concurrency isolation specs)
Spec: `postgres/src/test/isolation/specs/inherit-temp.spec`
Status: **foundation landed; multi-site wiring DEFERRED** (spec stays `defer`).

## Problem

`inherit-temp` builds an inheritance tree whose children are *temporary* tables
created in different sessions:

```
CREATE TABLE inh_parent (a int);
-- session s1:  CREATE TEMPORARY TABLE inh_temp_child_s1 () INHERITS (inh_parent);
-- session s2:  CREATE TEMPORARY TABLE inh_temp_child_s2 () INHERITS (inh_parent);
```

In PostgreSQL each backend owns its own temp namespace (`pg_temp_N`). When `s1`
runs `SELECT a FROM inh_parent`, inheritance expansion includes `inh_parent` and
`inh_temp_child_s1` but **excludes** `inh_temp_child_s2` — it lives in another
backend's namespace (`RELATION_IS_OTHER_TEMP`, see
`expand_single_inheritance_child` / `find_inheritance_children` in
`src/backend/optimizer/util/inherit.c` and `src/backend/catalog/pg_inherits.c`).
The same exclusion applies to `UPDATE` / `DELETE` / `TRUNCATE` routed through the
parent.

goopg keeps **all** relations — permanent and temporary, from every session — in
one shared in-memory catalog, and registers every `INHERITS` child against the
parent regardless of which session created it. So `s1`'s scan of `inh_parent`
picks up `inh_temp_child_s2` too, and `s1_select_p` returns 6 rows where PG
returns 4 (probe: expected 277 lines / got 276; first divergence at the first
`(4 rows)` footer). Every permutation that reads or writes the parent diverges.

## Why a foundation slice (not a full promotion) this loop

A faithful fix is **multi-site**: the parent→children expansion that must learn
the RELATION_IS_OTHER_TEMP rule happens in the planner (SELECT) **and** at
several executor write paths. Enumerated sites (all already call
`InheritanceChildren` / `PartitionChildren`):

| path | site |
|------|------|
| SELECT (planner) | `internal/planner/planner.go:2189` `collectInheritanceDescendants` (and `:2142` `collectAllPartitionLeaves`) |
| UPDATE            | `internal/executor/operators_storage.go:3122` |
| DELETE            | `internal/executor/operators_storage.go:3836`, `:4658` |
| UPDATE … FROM     | `internal/executor/operators_storage.go:4252` |
| TRUNCATE / DDL    | `internal/executor/operators_ddl.go` (`InheritanceChildren` recursion sites) |
| FK / MERGE / VACUUM | `operators_fk.go`, `operators_merge.go`, `operators_vacuum.go` |

Per the project's **sibling-paths discipline** (a green test on one expansion
path proves nothing about the others; a missed site is a *silent row-count*
regression — this repo's most expensive failure mode), these must be wired in
**one** atomic change with full gates (regress-port + pgbench + race + the
9-permutation spec byte-for-byte). That is a dedicated loop, not a side effect of
the central catalog change. This loop lands the **central, zero-blast-radius
foundation** (the established "Part A landed, not wired" pattern, cf. M0117-0006
Part A), so the wiring loop is a mechanical fan-out over a single shared helper.

## What landed (foundation)

1. **`catalog.Table.TempOwner string`** (`internal/catalog/catalog.go`) — the
   owning session's stable token. Empty for permanent/unlogged tables and for
   temp tables created without a session identity (internal/test contexts);
   consumers treat an empty-owner temp relation as visible to all sessions to
   preserve legacy single-session behaviour.

2. **Shared filter helper** `catalog.AccessibleInheritanceChildren(children,
   sessionTempOwner)` (`internal/catalog/catalog.go`) — the single chokepoint the
   wiring loop calls at every expansion site. Drops `c.Temp && c.TempOwner != ""
   && c.TempOwner != sessionTempOwner`; keeps permanent children, own temp
   children, and unowned temp children. Returns the same backing slice when
   nothing is dropped; nil-in / nil-out. Unit-tested
   (`inherit_temp_owner_test.go`).

3. **Stable per-session identity** `config.SessionRegistry.UniqueID() uint64`
   (`internal/config/session.go`) — a process-unique, non-zero, lifetime-stable
   id minted per connection from an atomic counter. Both the CREATE site
   (executor) and the SELECT site (planner, via `sessionPlanCatalog`'s `sess`)
   already hold the same `*SessionRegistry`, so they agree on the token. Unit
   tested (`session_uniqueid_test.go`).

4. **Owner-token derivation + stamping** `executor.sessionTempOwner(ctx)`
   (`internal/executor/context.go`) reads the token from
   `ctx.AdvisorySessionIdentity` (the per-connection `SessionRegistry`) via a
   minimal `UniqueID() uint64` interface and formats it `"s<id>"`. Both
   `CREATE TEMPORARY TABLE` paths (`operators_ddl.go` base table + partition
   leaf) now stamp `tbl.TempOwner = sessionTempOwner(o.ctx)` when
   `s.Temporary`. Unit tested (`inherit_temp_owner_test.go`).

Nothing reads `TempOwner` in the live read/write paths yet, so behaviour is
unchanged (blast radius nil): permanent inheritance is untouched, and a temp
child created and queried within the same connection carries a matching token.

## Wiring plan (the deferred follow-up — resume point)

In one loop, at each site in the table above, replace the raw children list with
`catalog.AccessibleInheritanceChildren(children, owner)`:

- **executor sites**: `owner = sessionTempOwner(ctx)` (ctx in hand).
- **planner SELECT site**: thread the token from the session catalog wrapper.
  `sessionPlanCatalog(sess, …)` (`internal/server/dispatch.go:870`) must expose
  the session's `"s"+UniqueID()`; add a small `CurrentTempOwner() string` channel
  on the wrapper and a `currentTempOwner(cat)` walk in
  `planScanRangeVar`, then pass it into `collectInheritanceDescendants` /
  `collectAllPartitionLeaves`.

Gates for the wiring loop: `TestPort_IsolationInheritTemp` strict (9 perms,
byte-for-byte — note the last two permutations also assert TRUNCATE-on-parent
blocking semantics: `s2_select_p` waits behind `s1`'s in-progress `TRUNCATE
inh_parent` while `s2_select_c` on its own temp child does not), full
`TestPort_Isolation*` regression, executor/planner units, regress-port,
`-race` executor, pgbench smoke.

## Tests (this loop)

- `internal/catalog/inherit_temp_owner_test.go::TestAccessibleInheritanceChildren`
- `internal/config/session_uniqueid_test.go::TestSessionRegistryUniqueID`
- `internal/executor/inherit_temp_owner_test.go::TestSessionTempOwner`
- Full `internal/catalog`, `internal/config`, `internal/executor` packages green.

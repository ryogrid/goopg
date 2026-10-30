# Publication / Subscription Catalog (Milestone 0008)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0008 — Logical Replication Support                     |
| Refines    | [0008-0001-logical-decoding-pipeline.md](0008-0001-logical-decoding-pipeline.md), [0008-0002-pgoutput-plugin.md](0008-0002-pgoutput-plugin.md) |
| Supersedes | —                                                      |

## Problem

`0008-0001` and `0008-0002` deliver the WAL → reorder buffer →
pgoutput pipeline. The next missing surface is the **operator**
view: `CREATE PUBLICATION` / `CREATE SUBSCRIPTION` on the wire,
plus the catalog tables that expose the configured shape:
`pg_publication`, `pg_publication_rel`, `pg_publication_tables`,
`pg_subscription`, `pg_subscription_rel`.

Per the M0008 milestone, this design-doc seam covers parser surface,
catalog tables and views, and the lifecycle of `CREATE
SUBSCRIPTION` and the slot it provisions on the publisher.

The full DDL surface plus the apply-worker / tablesync coordination
is a multi-loop story. **This first slice** delivers the in-memory
catalog substrate + the five virtual views; the parser surface, the
`CREATE PUBLICATION`-from-SQL path, and slot-provisioning
handshake follow in subsequent loops.

## Decision

### In-memory registry: `internal/catalog/pubsub.go`

A new `PubSub` type carries the publication and subscription state
in process. Single-process, mutex-guarded, hung off the existing
`*Runtime` so a `goopg start` boot makes it visible to both the
SQL surface (when wired) and the virtual views.

```go
type PubSub struct {
    mu            sync.RWMutex
    publications  map[string]*Publication
    subscriptions map[string]*Subscription
}

type Publication struct {
    Name          string   // pg_publication.pubname
    AllTables     bool     // FOR ALL TABLES
    PublishInsert bool     // publish=insert (default true)
    PublishUpdate bool
    PublishDelete bool
    Tables        []string // qualified table names; ignored when AllTables
    OID           uint32
}

type Subscription struct {
    Name           string // pg_subscription.subname
    Conninfo       string // CONNECTION '…'
    Publications   []string
    Enabled        bool
    SlotName       string // defaults to the subscription name
    OID            uint32
}
```

The `OID` fields use `catalog.FirstUserOID + n` allocations to keep
the OID space disjoint from user tables; the M0008 milestone DoD
doesn't care about exact OID values (operators read names), but
keeping them stable across a session is what `pg_publication.oid`
joins demand once a real apply worker queries them.

### Virtual catalog views

Five views in `internal/initdb/replication_views.go`, registered
in `Open` next to `pg_replication_slots`:

| View                      | Backing source                                              | Columns (upstream PG 18.x)                                                                  |
| ------------------------- | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `pg_publication`          | `PubSub.publications`                                       | `oid, pubname, pubowner, puballtables, pubinsert, pubupdate, pubdelete, pubtruncate, pubviaroot` |
| `pg_publication_rel`      | `Publication.Tables` per pub × per-table OIDs               | `oid, prpubid, prrelid, prqual, prattrs`                                                    |
| `pg_publication_tables`   | Resolved view across `pg_publication` × catalog.AllTables   | `pubname, schemaname, tablename, attnames, rowfilter`                                       |
| `pg_subscription`         | `PubSub.subscriptions`                                      | `oid, subdbid, subskiplsn, subname, subowner, subenabled, subbinary, substream, subtwophasestate, subdisableonerr, subpasswordrequired, subrunasowner, subfailover, suborigin, subconninfo, subslotname, subsynccommit, subpublications, suborigin` |
| `pg_subscription_rel`     | (deferred until tablesync; emits zero rows in this loop)    | `srsubid, srrelid, srsubstate, srsublsn`                                                    |

Columns goopg doesn't yet track (`pubowner`, `pubviaroot`,
`subbinary`, `substream`, `subtwophasestate`, etc.) emit empty
strings / `f` / `0` consistent with the rest of the M0008 view
surface.

### Why catalog substrate first

The milestone's SQL surface needs five catalog tables to be
queryable by name. A naive plan ("ship parser first, hide the
view behind a feature flag") would make the substrate's shape
follow the parser's needs rather than the catalog's — the wrong
direction. Landing the registry + views first means:

- `psql \dRp` against a goopg server already returns clean (empty)
  rows from `pg_publication` once this loop ships.
- Future parser loops just call `runtime.PubSub.CreatePublication(...)`
  and the views update automatically.
- The view column shape — frozen here — is what the apply worker
  will eventually read.

### Wiring through `Runtime`

`*initdb.Runtime` grows a `PubSub *catalog.PubSub` field. `Open`
constructs it with `catalog.NewPubSub()` and passes it into the
new view registrar. The pubsub registry has no on-disk state in
this loop — it's reset on every `goopg start`. Persistence
follows in a later loop alongside the rest of the M0008 catalog
work (the on-disk shape will round-trip through the existing
`<DataDir>/global/pg_catalog.json` snapshot path).

### What this loop doesn't deliver

- **Parser surface.** `CREATE PUBLICATION` / `CREATE SUBSCRIPTION`
  syntax, `ALTER PUBLICATION ADD TABLE`, etc. Tracked as the next
  M0008 / 0008-0003 loop.
- **Wire integration.** SQL paths still receive the existing
  `compatNoopCommandTag` shim for these statement shapes; the
  views populate via tests / future parser code, not yet via SQL.
- **Slot provisioning at CREATE SUBSCRIPTION time.** Lands with
  the apply worker (0008-0004), which depends on this catalog
  substrate.
- **Persistence across server restart.** Future loop.
- **`pg_subscription_rel` real rows.** The tablesync state is
  driven by the apply worker; until it lands the view emits zero
  rows.
- **`prattrs` / `prqual` (column lists / row filters).** M0008
  out-of-scope: "Whole-row, whole-set replication only."

## Verification

`internal/catalog/pubsub_test.go`:

- `TestPubSubCreatePublicationByTable` — `CreatePublication("p1",
  ["public.items"])` is retrievable via `Publications()`,
  `LookupPublication("p1")`, and the per-OID list of
  `PublicationTables()`.
- `TestPubSubCreatePublicationAllTables` — `AllTables=true`
  publication carries an empty Tables slice and reports
  `puballtables = 't'` to the view.
- `TestPubSubDropPublication` — drop removes the publication and
  its rels.
- `TestPubSubCreateSubscription` — register a subscription with
  conninfo + publication list; round-trips through
  `Subscriptions()`.
- `TestPubSubDuplicateNames` — `CreatePublication("p1", …)` twice
  fails with `ErrPublicationExists`.

`internal/initdb/replication_views_test.go`:

- `TestPgPublicationViewRendersRows` — register one publication
  via `Runtime.PubSub`, query the view, confirm one row with the
  right shape.
- `TestPgSubscriptionViewRendersRows` — same shape pin for
  subscriptions.

End-to-end SQL exercise lands once the parser surface ships in
the next loop.

## Cross-references

- Milestone: `docs/milestones/0008-logical-replication-support.md`.
- Pipeline foundation: `0008-0001-logical-decoding-pipeline.md`.
- Output plugin: `0008-0002-pgoutput-plugin.md`.
- Apply worker (consumes this catalog): `0008-0004-apply-worker-and-tablesync.md`
  (planned).
- Upstream:
  - `postgres/src/include/catalog/pg_publication.h`,
    `pg_publication_rel.h`,
    `pg_subscription.h`, `pg_subscription_rel.h` — column shapes.
  - `postgres/src/backend/commands/publicationcmds.c`,
    `subscriptioncmds.c` — DDL semantics this loop targets for
    eventual parity.

## Update 2026-10-30 — ALTER PUBLICATION ... ADD / SET / DROP TABLE (M0122-0014, `2738001df`)

PG 18.3 reference: `postgres/src/backend/parser/gram.y` (AlterPublicationStmt, pub_obj_list),
`postgres/src/backend/commands/publicationcmds.c` (AlterPublicationTables, CheckAlterPublication, OpenTableList),
`postgres/src/backend/catalog/pg_publication.c` (publication_add_relation).

Every ALTER PUBLICATION form other than OWNER TO used to be a `CompatNoopStmt`. That includes `ALTER PUBLICATION p
ADD TABLE ONLY s.t`, which is how pg_dump writes publication membership, so a restored dump lost all of it.

**Parser** (`internal/parser/ddl.go`): `{ ADD | SET | DROP } TABLE [ONLY] t [*] [, ...]` becomes
`AlterPublicationTablesStmt`. An item with a column list, a row filter, or a TABLES IN SCHEMA entry keeps the whole
statement the compat no-op it was. goopg models none of those features, and a syntax error on SQL PG accepts would be
worse.

**Executor** (`execAlterPublicationTables`):

| case | result |
|---|---|
| publication does not exist | 42704 |
| FOR ALL TABLES publication | 55000, with detail "Tables cannot be added to or dropped from FOR ALL TABLES publications." |
| table does not exist | 42P01 |
| ADD of a member | 42710 "relation \"%s\" is already member of publication \"%s\"" |
| DROP of a non-member | 42704 "relation \"%s\" is not part of the publication" |
| SET | replaces the list |

Tables named twice are folded (OpenTableList). The registry update is `PubSub.SetPublicationTables`. Membership is
journalled as pg_publication_rel rows: `writePublicationMemberRowsFor` for added tables and
`deletePublicationMemberRows` (xmax) for dropped ones. The startup reload rebuilds the registry from those rows; a
live probe across a restart keeps the SET result.

**Virtual view fix:** pg_publication_rel's `prqual` / `prattrs` were `""`, so psql's `\dRp+` printed every member
as `"public.t" () WHERE`. They are now `VirtualNull`.

**Pins:** `TestAlterPublicationTables`, `TestParseAlterPublicationTables`,
`TestParseAlterPublicationUnmodelledTableFormsStayNoop`.

**regress publication** goes from 2209 to 2213 diff lines.

- Now matching PG: the FOR ALL TABLES errors, the member listings, and the deferrable-key replica-identity error.
- Offsetting them: ALTERs on publications whose CREATE uses unmodelled features (TABLES IN SCHEMA, row filters) now
  fail visibly, and a DROP fails after an ADD whose column list kept it a no-op.

**Still open (ledgered):**

- TABLES IN SCHEMA, column lists and row filters.
- `ALTER PUBLICATION ... SET (option ...)` and RENAME TO are still no-ops.
- `CREATE PUBLICATION ... FOR TABLE ONLY t` fails to parse.
- `DROP PUBLICATION a, b` is not parsed.
- The ownership check (must be owner of publication) is not applied.

## Update 2026-10-30 — SET ( options ), DROP name lists, FOR TABLE ONLY (M0122-0014, `32bbaaf04`)

- **ALTER PUBLICATION name SET ( ... )** was a compat no-op. It now parses into `AlterPublicationOptionsStmt`, and
  `execAlterPublicationOptions` follows AlterPublicationOptions: the options are applied over the current ones
  (`publish` resets all four actions; an option not named keeps its value), `PubSub.SetPublicationOptions` updates
  the registry, and `upsertPublicationCatalogRow` rewrites the pg_publication row, which survives a restart. CREATE
  and ALTER share `applyPublicationOptions`, which ports parse_publication_options for the options goopg models:

  | input | result |
  |---|---|
  | bad `publish` value | 42601 "unrecognized value for publication option \"publish\": \"%s\"" (was 22023 "unrecognized publish action") |
  | unknown parameter | 42601 "unrecognized publication parameter: \"%s\"" |
  | publish\_via\_partition\_root, publish\_generated\_columns | accepted, not tracked |

- **DROP PUBLICATION** now takes gram.y's name list (`pub_name_list` in `goopg_ext.y`;
  `DropPublicationStmt.Names`, with `Name` = the first). Every name is resolved before any is dropped. PG's DROP is
  transactional and a missing name aborts it whole; goopg's registry drop is not, so `DROP PUBLICATION a, nosuch`
  must leave `a` in place. IF EXISTS skips a missing name with PG's NOTICE "publication \"%s\" does not exist,
  skipping" (it used to skip silently).
- **CREATE PUBLICATION ... FOR TABLE** items are `[ONLY] name [*]`, and a later item may repeat TABLE (pub\_obj\_list).
  `FOR TABLE ONLY t` used to be a syntax error.

**Pins:** `TestPublicationDDLForms` (validate-before-drop mutation caught), `TestParsePublicationDDLForms`.
`parity_goldens` gains `Names=["p"]` on its one DROP PUBLICATION entry.

**regress publication:** 2213 → 2208.

**Still open (ledgered):** TABLES IN SCHEMA, column lists and row filters; RENAME TO; the publication ownership
check; publish\_via\_partition\_root and publish\_generated\_columns are not tracked (pg\_publication shows `f` / `n`);
the `WITH (foo)` value-less option is a parser error, where PG reports "unrecognized publication parameter".

## Update 2026-10-30 — publisher-side slot lifecycle: CREATE/DROP SUBSCRIPTION (M0122-0014, `5657546d8`)

- **CREATE SUBSCRIPTION created no slot on the publisher.** CreateSubscription calls `walrcv_create_slot` when
  connect and create_slot (both default true) are on, and reports NOTICE "created replication slot \"%s\" on
  publisher". goopg skipped it, so the apply worker's START_REPLICATION failed "replication slot \"s\" does not
  exist" on every retry — a goopg subscriber of a goopg publisher received nothing (the delivery gap ledgered
  2026-10-30). The executor now calls a `CreateSubscriptionSlot` hook **before** the registry insert (PG's catalog
  row rolls back with a failed CREATE; goopg's registry insert does not). `postmaster.subscriptionSlotHooks` maps
  failures onto subscriptioncmds.c: unreachable publisher → 08006 "subscription \"%s\" could not connect to the
  publisher"; a refused CREATE_REPLICATION_SLOT → 08P01 "could not create replication slot \"%s\"".
- **DROP SUBSCRIPTION** follows DropSubscription's order: registry entry removed → worker stopped → slot dropped
  on the publisher (`DropSubscriptionSlot` → `DROP_REPLICATION_SLOT "s" WAIT`, ReplicationSlotDropAtPubNode with
  missing\_ok = false). `ApplyLauncher.StopWorker` mirrors logicalrep_worker_stop (cancel + join) so the next
  reconcile does not restart the worker and the slot is not active. A publisher-side failure restores the registry
  entry (PG's DROP rolls back) — a slot left behind pins WAL on the publisher forever.
- **DROP_REPLICATION_SLOT ... WAIT** on the walsender: `Slots.Drop` retried while the slot is owned
  (`xlog.ErrSlotInUse`), bounded at 5 min where PG's ReplicationSlotDrop wait is interruptible and unbounded
  (the handler has no client context to notice a vanished client).
- **slot_name = NONE layering fix.** `CreateSubscriptionAsOwner` defaulted an empty slotName to the subscription
  name, so NONE was stored as a real slot name: DROP chased a phantom slot on the (unreachable) publisher, and
  pg\_subscription showed a subslotname PG stores NULL. DefineSubscription's name default belongs to the
  executor (it applies it); the registry stores slotName as given — "" is NULL subslotname, rendered NULL by
  `buildPGSubscriptionRow` and round-tripped through the heap journal/reload path.

**Pins:** `TestSubscriptionPublisherSlotHooks` (create-gate on connect∧createSlot, no subscription on failed
create, registry restore on failed drop, IF EXISTS NOTICE); `TestPgSubscriptionViewRendersRows` NONE arm.
**Ports:** `TestPort_PgoutputInteropGoopgToGoopg` (slot exists on the publisher after CREATE, INSERT/UPDATE/
DELETE deliver — UPDATE of a non-key column lands through the Relation message's identity flags — slot gone
after DROP); `TestPort_PgoutputInteropPGToGoopgSlotLifecycle` (PG 18.3's own pg_replication_slots as oracle:
pgoutput/logical appears on CREATE, vanishes on DROP).

**Movement:** none — subscription DDL / replication; the plan instruments are blind to it (sf025 PLAN-SHAPE
same=99, arm 24 MATCH). regress publication unaffected.

**Still open (ledgered):** ALTER SUBSCRIPTION ... SET (slot\_name = NONE) / ENABLE / DISABLE / REFRESH PUBLICATION
are not implemented — the escape hatch PG documents for a subscription whose publisher is unreachable is
unavailable, so a dummy-conninfo subscription in a test must be created fully disassociated; the apply worker
does not confirm LSNs back on a DROP race; two-phase / streams / two\_phase options are accepted, not modelled.

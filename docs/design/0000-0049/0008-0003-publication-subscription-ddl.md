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

## Update 2026-10-31 — ALTER SUBSCRIPTION escape hatch: ENABLE / DISABLE / SET (slot\_name) (M0122-0014)

- **The trio DROP's 08006 hint names.** The 2026-10-30 slot-lifecycle slice left DROP SUBSCRIPTION raising 08006
  with PG's hint "use ALTER SUBSCRIPTION … DISABLE, then ALTER SUBSCRIPTION … SET (slot\_name = NONE)" — but
  neither statement existed, so the documented escape hatch for a subscription whose publisher is unreachable was
  unusable (ledgered 2026-10-30, item (a) of the slot-lifecycle row). This slice implements
  AlterSubscription's `ALTER_SUBSCRIPTION_ENABLED` and `ALTER_SUBSCRIPTION_OPTIONS` kinds
  (subscriptioncmds.c:1339-1357 and 1159-1341), so the sequence DISABLE → SET (slot\_name = NONE) → DROP
  SUBSCRIPTION runs end to end against a publisher that never answers: DISABLE stops the apply worker
  (launcher reconcile), the worker's exit releases the publisher slot, the NONE write disassociates it
  ("" = NULL subslotname), and DROP sees `SlotName == ""` and skips the publisher call.
- **Parser.** `AlterSubscriptionEnableStmt` / `AlterSubscriptionSetStmt` (ast.go), parsed in parseAlter's
  subscription tail: `enable`/`disable` are unreserved, taken as idents exactly like ALTER EVENT TRIGGER … ENABLE;
  `SET` requires a following `(` (KwSet arrives as a keyword token), so `SET PUBLICATION`, `REFRESH PUBLICATION`,
  `SKIP (lsn …)`, `RENAME TO` and `OWNER TO` keep their existing arms (the compat no-op, and OWNER TO's real one).
  `parsePubSubWithList` gains an `allowEmpty` flag for ALTER's `SET ()` — legal upstream (parse\_subscription\_options
  over an empty DefElem list, the tuple is rewritten unchanged) — while CREATE's `WITH ()` stays a parse error.
- **Registry + heap.** `PubSub.SetSubscriptionEnabled` / `SetSubscriptionSlotName` (pubsub.go) flip the row under
  the registry mutex, mirroring SetSubscriptionOwner; the executor re-syncs the pg\_subscription heap row through
  `syncSubscriptionRow` (subenabled / subslotname are rendered by `buildPGSubscriptionRow`), so the ALTER survives a
  restart via `reloadSubscriptionsFromHeap` (cols 5 and 14).
- **Executor arms, error-for-error (subscriptioncmds.c).** 42704 `subscription "x" does not exist` on both arms;
  55000 "cannot enable subscription that does not have a slot name" (1344-1348 — a `slot_name = NONE` subscription
  cannot be re-enabled); 55000 "cannot set slot\_name = NONE for enabled subscription" (1172-1178, PG's exact
  two-part message); the option scan applies ALTER's own `supported_opts` (slot\_name, synchronous\_commit, binary,
  streaming, two\_phase, disable\_on\_error, password\_required, run\_as\_owner, failover, origin — subscriptioncmds.c:
  1159-1162), so the CREATE-only options (connect, create\_slot, enabled, copy\_data) are 42601 "unrecognized
  subscription parameter", values run through CREATE's `defGet*` twins, origin keeps the 22023 arm. A new slot name
  is written straight through as PG's `namein` write does — ALTER validates the slot name nowhere (PG validates at
  START\_REPLICATION), unlike the CREATE arm's `validateReplicationSlotName`.
- **Launcher kick.** ENABLE/DISABLE call `OnSubscriptionChange` (the apply launcher's Wake → reconcile: disabled
  subs drop out of the enabled set, `reconcile` stops their workers, `!s.Enabled` guard). PG wakes at commit only
  for ENABLE (subscriptioncmds.c:1355); goopg also wakes on DISABLE — the deviation the design records, because
  the escape hatch needs the worker (and the publisher's slot lease) released *now*, not at the launcher's next
  reload.
- **Pins:** `TestParseAlterSubscriptionEscapeHatch` (statements + the no-op tails + PUBLICATION not taking the
  subscription-only arms), `TestAlterSubscriptionEscapeHatch` (the error arms + the DISABLE→NONE sequence + the
  accepted-not-modelled options + `SET ()` no-op), `TestPubSubSetSubscriptionEnabledAndSlot`, two
  `TestDDLCommandTagMatchesPostgres` rows for the CommandComplete tag.

**Movement:** none — subscription DDL; the plan instruments are blind to it (sf025 PLAN-SHAPE same=99, arm 24 MATCH).

**Still open (ledgered):** the accepted-not-modelled options are validated then dropped — pg\_subscription renders
defaults (subbinary f, substream f, subtwophasestate d, subsynccommit off, suborigin any) whatever ALTER SET wrote;
synchronous\_commit is not validated through `set_config_option`; the namein 63-byte truncation and the
"failover requires binary = true" check are not reproduced; ALTER SUBSCRIPTION … REFRESH PUBLICATION / SET / ADD /
DROP PUBLICATION / RENAME TO / CONNECTION / SKIP stay compat no-ops (REFRESH needs AlterSubscription\_refresh's
tablesync machinery); the apply worker still sends no standby status updates, so a live slot's confirmed\_flush
stays at its initial value.

## Update 2026-10-31 — ALTER SUBSCRIPTION SET / ADD / DROP PUBLICATION (M0122-0014)

- **The statement half of the publication-list arms.** gram.y:10873-10901's three bare-name_list forms —
  `ALTER SUBSCRIPTION name { SET | ADD | DROP } PUBLICATION name_list [WITH (…)]` (AlterSubscription's
  `ALTER_SUBSCRIPTION_{SET,ADD,DROP}_PUBLICATION` kinds, subscriptioncmds.c:1374-1475) — were compat no-ops.
  This slice lands the **statement side**: parse, list algebra, option validation, the refresh gates, the
  registry write and the pg\_subscription heap re-sync. The data half (AlterSubscription\_refresh) stays
  ledgered — see "Still open" below; `REFRESH PUBLICATION` remains a compat no-op.
- **Parser.** New `AlterSubscriptionPublicationsStmt{Name, Action, Publications, Options}` (ast.go). The
  subscription tail's `SET` arm branches on the next token: `(` takes the OPTIONS arm as before, `PUBLICATION`
  takes the list arm; `ADD`/`DROP` are new keyword arms taken only when followed by `PUBLICATION`, so
  `ALTER PUBLICATION … ADD TABLE …` keeps its own arm and every other subscription tail (`REFRESH`, `SKIP`,
  `RENAME TO`) stays the compat no-op. `parseAlterSubscriptionPubsTail` shares CREATE's ident-name convention;
  the WITH block reuses `parsePubSubWithList(false)` (non-empty, like CREATE's).
- **List algebra is PG's verbatim** (merge\_publications :2402 / check\_duplicates\_in\_publist): SET replaces
  (duplicate in the new list → 42710 `publication name "x" used more than once`); ADD appends, refusing a name
  already present (42710 `publication "x" is already in subscription "s"`); DROP removes, refusing a name not
  in the list (42704 `publication "x" is not in subscription "s"`) and the drop-to-empty case (42704 `cannot
  drop all the publications from a subscription`). **PG ordering law found in testing:** the list algebra runs
  *before* the refresh gates, so on a single-publication subscription a `DROP PUBLICATION` of that name raises
  the 42704 drop-to-empty error, not the disabled-55000 — pinned.
- **Refresh gates** (`refresh` defaults true; the arms' `supported_opts` are refresh + copy\_data, anything
  else 42601 "unrecognized subscription parameter", values through CREATE's `defGetSubscriptionBoolean`
  twin): disabled subscription → 55000 "ALTER SUBSCRIPTION with refresh is not allowed for disabled
  subscriptions" with the per-action hint PG writes at :1392-1408/1440-1456 (SET's period-terminated form,
  ADD/DROP's "… WITH (refresh = false) instead."); explicit transaction block → 25001 "ALTER SUBSCRIPTION
  with refresh cannot run inside a transaction block" (PreventInTransactionBlock, goopg's
  `Session.InExplicitTransaction()` pattern). `copy_data` validates, is accepted-not-modelled (the ledgered
  refresh half's COPY would consume it).
- **State landing.** New `PubSub.SetSubscriptionPublications` (pubsub.go) swaps the registry list under the
  mutex; `syncSubscriptionRow` rewrites the pg\_subscription heap row's `subpublications` text[]
  (`buildPGSubscriptionRow` renders it; the heap-reload round-trip decodes via `ParseTextArrayLiteral`,
  already pinned). `OnSubscriptionChange` wakes the launcher like the ENABLE/DISABLE arms; the running apply
  worker keeps its current stream — the new list reaches the publisher on the worker's next
  START\_REPLICATION, where `publication_names` is sent (eager restart ledgered).
- **Pins:** `TestParseAlterSubscriptionPublications` (three arms, WITH tails, OPTIONS-arm non-take, ALTER
  PUBLICATION non-take, empty-list parse error), `TestAlterSubscriptionPublications` (mutation algebra +
  order preservation, all five merge refusals, the disabled-gate hints per action + the refresh=false escape
  hatch, the 42601/Boolean option arms, the BEGIN/ROLLBACK 25001 arm, the ordering pin), three
  `TestDDLCommandTagMatchesPostgres` rows. Two stale CompatNoop expectations for `SET PUBLICATION p2` were
  updated (both parser tests).

**Movement:** none — subscription DDL; the plan instruments are blind to it (spotcheck 20/20, arm 24 MATCH,
fireset all-clear).

**Still open (ledgered):** the data half — the publisher-side check that the new publications exist
(`publication %s does not exist on the publisher`, check\_publications\_exist :498, needs a publisher
round-trip), AlterSubscription\_refresh's pg\_subscription\_rel 'i' rows + per-table COPY + tablesync state
machine (the `internal/replication` tablesync library is unwired in production), the eager apply-worker
restart on a list change (PG's 25001 gate exists precisely because it restarts the worker inside the
command), and copy\_data's modelled effect.

## Update 2026-10-31 — ALTER SUBSCRIPTION publication-list refresh half: publisher dial + check\_publications (M0122-0014)

- **What the statement-side slice left open, item (1).** The 2026-10-31 slice above ledgered
  AlterSubscription\_refresh's publisher half. This slice lands its **first two steps** for all three
  publication-list arms when refresh = true: PG's `walrcv_connect(sub->conninfo)` (:838-850) and
  `check_publications` (:443-501). The rest — `fetch_table_list`, the pg\_subscription\_rel 'i' rows, the
  per-table COPY through the tablesync machine, the eager worker restart — stays ledgered (the library is
  still unwired); `REFRESH PUBLICATION` itself stays a compat no-op.
- **Client.** `replication.CheckRemotePublications(ctx, cfg, pubs)` (logicalreceiver.go) is the
  walrcv\_exec twin of `runRemoteReplicationCommand`: dial (replication=database, the same
  LogicalReceiver.connect handshake the slot path uses), write PG's exact
  `SELECT t.pubname FROM pg_catalog.pg_publication t WHERE t.pubname IN ('a', 'b')` (names through a
  `quote_literal_cstr` twin), collect DataRows via the existing `decodeDataRow`, return the requested
  names the publisher lacks, in request order. An empty pubs list is **connect-only**: ALTER … DROP
  PUBLICATION passes validate\_publications = NULL (:1431), yet AlterSubscription\_refresh dials anyway
  (fetch\_table\_list follows), so a DROP's refresh requires a reachable publisher. Unreachable publisher →
  ErrPublisherConnect-wrapped; publisher's ErrorResponse → *RemoteCommandError.
- **Server hook.** `subscriptionSlotHooks` gains its third arm (subscription\_slot.go):
  `ectx.CheckPublicationsOnPublisher` maps dial failure → **08006** "subscription %q could not connect to
  the publisher: %s" (ERRCODE\_CONNECTION\_FAILURE, the same code arm the CREATE slot path measures live)
  and query failure → **XX000** "could not receive list of publications from the publisher: %s"
  (:455-459 carries no errcode). Wired at both executor-context build sites next to the slot hooks; nil
  (test contexts) skips the publisher side, exactly like the slot hooks.
- **Executor arm.** In `execAlterSubscriptionPublications`, between the refresh gates and the list write:
  SET/ADD validate the STATEMENT list (`stmt->publication`), DROP validates none. A missing publication is
  a **WARNING, not an error** (:493-501, `ereport(WARNING)` + `errmsg_plural`) — the statement lands and
  the name goes into subpublications; the refresh machinery would skip that publication's tables. Singular
  `publication "x" does not exist on the publisher`; plural `publications "x", "y" do not exist on the
  publisher` (names double-quoted, comma-separated — GetPublicationsStr quote\_literal=false,
  pg\_subscription.c:41). A connect/query failure aborts before the registry write (PG updates the tuple
  only after the arm's refresh returns, :1558-1573).
- **Publisher side (no change needed).** The walsender's dispatcher falls through non-replication verbs
  to the regular SQL handler (walsender.go header), so the plain SELECT on the replication=database
  connection answers from the virtual pg\_publication view — measured live, first try.
- **Pins:** `TestAlterSubscriptionPublications` grows the injected-hook arms (SET/ADD validate list,
  DROP's nil validate list, singular + plural WARNING texts via TakeNotices, the 08006 abort with the list
  untouched, refresh=false never dials); `TestPort_PgoutputInteropGoopgToGoopgAlterPubsRefresh` pins the
  live goopg→goopg wire: ADD of a publisher-known publication lands, ADD of a publisher-unknown
  publication **lands** (psc.Exec fails the test on any ERROR — pinning PG's non-error shape), DROP
  connect-only lands, SET replaces; observed through the pg\_subscription view's subpublications.
  The dead-publisher 08006 and the query-failure XX000 arms are pinned through the injected hook (the
  harness has one live publisher; a dead-publisher cluster is not in the harness).

**Movement:** none — subscription DDL; the plan instruments are blind to it (spotcheck, arm 24 MATCH,
fireset no fires, sf025 sweep 99/99).

**Still open (ledgered):** fetch\_table\_list over the dial (the publisher's table list for the new
publications — the same connection is already open in the hook today; the ledgered next step threads it),
pg\_subscription\_rel 'i' rows + per-table initial COPY + i→d→s (the unwired tablesync library), the
eager apply-worker restart on a list change, copy\_data's modelled effect, the
two\_phase=enabled + copy\_data 55000 arms (goopg does not model two\_phase), and the dead-publisher 08006 /
XX000 arms' live-cluster pin.

## Update 2026-11-01 — refresh data half: fetch\_table\_list, 'i' rows, initial COPY, worker restart (M0122-0014)

AlterSubscription\_refresh's data half (subscriptioncmds.c:820-880) lands for refresh=true
SET / ADD / DROP PUBLICATION. New `internal/postmaster/subscription_refresh.go`
(`RefreshSubscriptionOnPublisher`, wired through `subscriptionSlotHooks`' 4th arm →
`ectx.RefreshSubscriptionOnPublisher`):

1. **fetch\_table\_list** (:2103) over the dial — `FetchPublicationTables`
   (`internal/replication/logicalreceiver.go::queryRows` + `PubTable`) runs the PG16+
   `pg_publication` / `pg_publication_rel` / `pg_namespace` join over the publisher
   connection; a remote refusal maps to XX000, a dial failure to 08006 (same arms as the
   check\_publications hook above).
2. **Local resolution with the 42P01 arm** (:2176-2181) — every fetched name resolves
   through the search-path catalog wrapper BEFORE any `pg_subscription_rel` write; a
   published table missing on the subscriber aborts the statement ("table %s does not
   exist on the subscriber") with zero row changes.
3. **Row swap** — rows for tables that left the merged list drop
   (`RemoveSubscriptionRel`; PG TRUNCATEs the relations too — ledgered), entering tables
   seed 'i' rows (`create_subscription_rel`); `copy_data = false` marks them READY
   (i→s→r waypoints, no COPY).
4. **Initial COPY** — `copy_data = true` drives `RunTableSyncManager`: one fresh
   publisher connection + one `COPY <rel> TO STDOUT` → CopyFromExecutor exchange per
   non-'r' row (the tablesync workers' job); per-rel failures aggregate to XX000 after
   the sweep. Sync-end promotion advances completed rows 's'→'r' (LSN 1) so the
   relaunched worker's 's' gate doesn't swallow the first streamed commit.
5. **Ordering + restart** — PG updates the `pg_subscription` tuple AFTER the refresh
   returns (subscriptioncmds.c:834-879), so the executor calls the refresh hook BEFORE
   `SetSubscriptionPublications`: a 42P01/XX000/08006 abort leaves the publication list
   unchanged (the earlier "list-write-not-rolled-back" deviation is closed — a retried
   ADD of the same publication no longer hits its own 42710 duplicate guard). The hook
   kills the apply worker (`StopWorker`, PG's kill-inside-refresh); the executor's
   post-write launcher kick (`OnSubscriptionChange`) relaunches it dialing the committed
   list (PG's wake-after-commit). goopg restarts eagerly inside the command where PG
   lets the running stream end first (ledgered).

**Two replication-stream fixes landed on the way** (both were blockers for the
post-restart streaming pin):

* **Slot resume off-by-one** (`internal/access/transam/xlog/slot_decoder.go`):
  `ConfirmedFlushLSN` is the EXCLUSIVE resume boundary (the LSN the next
  START\_REPLICATION begins at — `NewRecordIterator` derives `pos = startLSN − 1`), but
  the decoder advanced it to `rec.EndLSN`, the commit record's LAST byte. Every relaunched
  logical stream therefore started one byte early, read the record header mid-tail, and
  died with "invalid record header: padding bytes nonzero" — which is what killed the
  apply worker after every eager restart (slot left ACTIVE, clean server STOP hanging
  behind the deadlocked session). The advance now stores `rec.EndLSN + 1`.
* **Walsender exit arms** (`internal/replication/logicalwalsender.go`): the
  `streamCtx.Done` and `runErr` select arms awaited `receiveDone`, but the reader
  goroutine parks in ReadFrame on a LIVE socket — the subscriber side stays connected
  across an eager worker restart — so both arms deadlocked forever whenever the decoder
  exited without the client disconnecting (ctx cancel = worker stop; decoder failure =
  any WAL/decode error). The arms now cancel the stream and return without joining the
  reader (the physical replyStartReplication loop never joins it either; the reader
  exits when the connection tears down), and a real decode error is surfaced to the
  client via `writeStreamingError` before returning.

**Pins:** `TestPort_PgoutputInteropGoopgToGoopgRefreshData` (goopg→goopg): ADD p1
initial-COPYs t's 5 pre-ADD rows, the row reaches 'r', a post-ADD INSERT streams through
the restarted worker; ADD p2 (subscriber lacks u) 42P01-aborts leaving NO row and NO
data and the publication list unchanged (the retry then lands cleanly); the retry COPYs
u's 3 rows and leaves two 'r' rows; DROP p1 keeps both rows (t stays published through
dummy) with t's data intact; DROP p2 removes u's row (u leaves every remaining
publication) keeping u's data. `TestIsPermanentClassifier` pins "replication slot is
active" (55006) as transient so a restart racing the old stream's slot release retries
with backoff instead of aborting. `TestPort_…AlterPubsRefresh` (loop #9) stays green.

**Gates (index-staged):** executor + replication + catalog + xlog + postmaster-arm unit
suites, tpch-spotcheck Q12=2 / Q13=35, TPC-H acceptance-arm 24/24 value MATCH, tpcds
sf025 sweep 99/99 plan-shape identical; fireset-scope OUT (no optimizer/planner/cost
paths); s9 census REUSE (routing-20261101).

**Still open (ledgered):** the CREATE SUBSCRIPTION initial-sync arm (rows + COPY at
commit, driven by the launcher today), ALTER … REFRESH PUBLICATION as a standalone
statement arm, the TRUNCATE-on-drop-tables half of RemoveSubscriptionRel,
subpublications / subrel heap persistence, the real sync-LSN recorded at 's' (the v0
transport carries LSN 1), the eager-restart window (PG lets the stream drain; goopg
kills in-command), and re-application of streamed transactions that raced the
initial COPY (PK dedup is the current safety net; PG's sync-LSN skip-forward is not
modelled).

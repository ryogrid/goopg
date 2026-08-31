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

# System-view OID policy — pin to upstream, or rewrite every captured blob

**Status:** accepted (implemented 2026-08-11, M0131-S8a)
**Date:** 2026-08-11
**Milestone:** M0131 (S8a)

## Problem

A captured `ev_action` blob is a serialised `Query` tree that names relations by
OID. For the six views hosted today that was almost free — grepping every
`internal/initdb/*_ev_action.dat` for `:relid` gives: none in
`pg_stat_wal_receiver` and `pg_stat_recovery_prefetch`; `1260` ×2 (pg_authid) in
`pg_stat_replication`; `1262` ×2 (pg_database) in `pg_replication_slots`;
`6100` ×2 (pg_subscription) in `pg_stat_subscription`; and **`12261` ×2** in
`pg_stat_replication_slots`.

`12261` is not a pinned catalog OID. It sits in
`FirstUnpinnedObjectId..FirstNormalObjectId` = 12000..16383
(`postgres/src/include/access/transam.h:195-197`), the band upstream's initdb
allocates from while executing `system_views.sql`. Its context in the blob
settles what it is:

```
:rtekind 0 :relid 12261 :inh true :relkind v :rellockmode 1 :perminfoindex 1
```

`relkind v`, and the RTE's `eref` colnames are exactly the 21 columns of
`pg_replication_slots` (`slot_name`, `plugin`, `slot_type`, `datoid`, …,
`failover`, `synced`) — matching `RelNatts: 21` on goopg's own
`pg_replication_slots` nailed row (`internal/initdb/relcache_init.go:696`). So
`12261` is **upstream's OID for `pg_replication_slots`**, where goopg assigns
`12105`. The bootstrap header comment claims *"No view-side relid appears in
the tree … so no OID rewriting is needed"*
(`internal/initdb/pg_rewrite_bootstrap.go:15-17`) — true for the other five,
false for this one. Not theoretical: the moment M0131-S6 flips `relhasrules`,
a hosted PG opening `pg_stat_replication_slots` tries to open relation 12261,
which does not exist in a goopg cluster.

### The forcing function: view-on-view is the norm, not the exception

`system_views.sql` defines **80** views. Scanning every definition body for
references to another view name in the same file finds **14** edges:

| dependent | base | lines |
|---|---|---|
| `pg_user` | `pg_shadow` | `:60` → `:35` |
| `pg_stat_sys_tables`, `pg_stat_user_tables` | `pg_stat_all_tables` | `:739`, `:749` → `:679` |
| `pg_stat_xact_sys_tables`, `pg_stat_xact_user_tables` | `pg_stat_xact_all_tables` | `:744`, `:754` → `:718` |
| `pg_statio_sys_tables`, `pg_statio_user_tables` | `pg_statio_all_tables` | `:793`, `:798` → `:759` |
| `pg_stat_sys_indexes`, `pg_stat_user_indexes` | `pg_stat_all_indexes` | `:820`, `:825` → `:803` |
| `pg_statio_sys_indexes`, `pg_statio_user_indexes` | `pg_statio_all_indexes` | `:846`, `:851` → `:830` |
| `pg_statio_sys_sequences`, `pg_statio_user_sequences` | `pg_statio_all_sequences` | `:868`, `:873` → `:856` |
| `pg_stat_replication_slots` | `pg_replication_slots` | `:1045` → `:1019` |

(all in `postgres/src/backend/catalog/system_views.sql`). Twelve of the fourteen
are literally `SELECT * FROM <base view> WHERE schemaname …`:

```sql
CREATE VIEW pg_stat_sys_tables AS
    SELECT * FROM pg_stat_all_tables
    WHERE schemaname IN ('pg_catalog', 'information_schema') OR
          schemaname ~ '^pg_toast';
```

and `pg_user` is `SELECT usename, usesysid, … FROM pg_shadow;` (`:60-71`). Every
one carries the base view's initdb-assigned OID inside its `ev_action`. S9.3 is
14 more instances of the `12261` problem, not one.

## Design

### Option A — pin goopg's system-view OIDs to upstream's initdb assignment

goopg stops choosing its own `12xxx` values and adopts whatever PG 18.3's
initdb assigns (`pg_replication_slots` → 12261, and so on for every view S9
captures). Captured blobs then need **no** relid rewriting at all.

**Is it feasible against goopg's allocator?** Yes, and more cleanly than the
plan assumes. goopg has *no* runtime allocator that can reach 12000..16383:

- `catalog.FirstUserOID = 16384` (`internal/catalog/catalog.go:3604`) is the
  floor for every dynamically minted relation OID — `pubsub.go:156` seeds
  `nextOID: FirstUserOID`; `routines.go:218` starts routines at
  `FirstRoutineOID = 1 << 17`; reload paths filter on the same floor
  (`catalog_heap_reload.go:190`, `:730`, `:780`; `catalog_cache.go:71`).
- Everything in the band is a hand-written constant: `nailedLocalRels` entries
  `12100`, `12102`–`12106` (`relcache_init.go:688`, `:693-697`) and rule OIDs
  `12101`, `12107`–`12111` (`pg_rewrite_bootstrap.go:52-59`). The rationale is
  explicit: *"12100 is chosen unilaterally and pinned by regression test"*
  (`relcache_init.go:670-675`).

So pinning is a rename of constants in a hand-maintained table, with no
allocator to collide with. Blast radius is small and enumerable: 42 occurrences
of `12100`–`12111` across six files (`relcache_init.go`,
`pg_rewrite_bootstrap.go`, `catalog_heap_reload.go:617` and `:674`,
`pg_replication_views_nailed_test.go`, `pg_stat_wal_receiver_nailed_test.go`,
`internal/estimateaudit/parity_test.go`).

**Costs, stated honestly.** goopg's view OIDs become a function of *upstream's
initdb execution order* — deterministic for a fixed PG 18.3 build, but a PG
18.4/19 catalog change that adds or reorders an object in `system_views.sql`
shifts the whole band, leaving goopg with OIDs matching no PG it ships against.
Mitigation: the pinned table is generated and re-verifiable by S7's tool
(`docs/design/0131-0007-ev-action-capture-tooling.md` guard #6), so the drift is
*detected*, not silent — but it is a real maintenance coupling to a specific
oracle version and must be ledgered as such. Two smaller costs: rule OIDs
(`pg_rewrite.oid`) must be pinned too, or a hosted PG's `RewriteOidIndexId`
(2692) lookups disagree with the tree's own `pg_rewrite` rows; and any
goopg-only view with no upstream counterpart needs a reserved disjoint sub-band
above the highest upstream assignment.

### Option B — keep goopg's OIDs, rewrite relids in every captured blob

S7's capture already needs an `oracleViewOID → goopgViewOID` table for the
`12261` case; Option B makes that table permanent and non-empty.

**Costs, stated honestly.** Every capture, forever, runs a rewriting pass — new
failure surface on the path that produces the corpus. The pass must distinguish
a *view* relid from a *catalog* relid, and numeric range is not a sound test on
its own: `:relid` is not confined to `RANGETBLENTRY` nodes — the
`:perminfoindex`-referenced `RTEPERMISSIONINFO` carries one too, which is
exactly why `12261` appears **twice** in one blob. The only sound discriminator
is a table of the oracle's view OIDs — **the same table Option A pins. Option B
does not avoid building the mapping table; it only avoids adopting it.**
Textual rewriting of a `nodeToString` stream is also byte-fragile:
`:relid 12261` must not match `:relid 122610`, and must not touch a `12261`
appearing as a `Const` value or inside a colname string, so tokenised rewriting
— a partial `pg_node_tree` parser in the tool — is required. Finally it
permanently forecloses the cheapest possible S7 acceptance test ("captured bytes
equal committed bytes"), because the committed bytes are by construction never
what the oracle produced.

### Recommendation: **Option A (pin)**

The decisive argument is reversibility asymmetry, not cost. Under A the S7
oracle test is `cmp` against upstream's own output and the mapping table is the
identity function, checked rather than applied. Under B the corpus is
permanently derived-not-captured and every future view inherits the rewriting
pass. Option A's one genuine cost — version coupling — is *detectable* with an
automated detector; Option B's is an unbounded, permanently live transformation
on the path that produces on-disk catalog bytes a foreign engine casts straight
to `FormData_*` structs.

My reading of the allocator **strengthens** the plan's recommendation rather
than contradicting it: there is no OID allocator to collide with, so the
"goopg must not collide with its own allocation" objection to pinning is void.
The remaining objection is version coupling, and that one is real.

Sequencing is unchanged: S8 must land **before** S9 captures anything, or the
corpus is redone. S6 may patch `pg_stat_replication_slots_ev_action.dat`'s
`12261 → 12105` as an interim measure; under Option A that patch is reverted
and the view OID moves `12105 → 12261` instead.

## Guards

1. **`nailedRel` view OIDs match upstream.** Walk `nailedSharedRels` +
   `nailedLocalRels`, select every entry with `RelKind == 'v'`, assert its `OID`
   equals the OID recorded for that view name in S7's manifest
   (`internal/initdb/nailed_view_manifest.tsv`, produced from a real PG 18.3
   `initdb`). This is the S8 acceptance test.
2. **Rule OIDs match upstream** — same check over `pgRewriteInitialEntries()`
   (`pg_rewrite_bootstrap.go:112-186`) against the manifest's rule-OID column.
3. **No unmapped in-band relid survives in any blob.** Scan every
   `internal/initdb/*_ev_action.dat` for `:relid`; fail on any value in
   12000..16383 that is not a pinned goopg view OID. Under Option A this must
   **fail today** on `pg_stat_replication_slots_ev_action.dat`'s `12261` and
   pass once that view is repinned — M0131-S6.2's invariant reached from the
   other direction.
4. **Disjointness.** No two nailed relations, and no relation and rule, share an
   OID; every goopg-only assignment lies outside the band upstream uses.
5. **Version stamp.** The manifest records the oracle's `SELECT version()` and
   `catversion`; a mismatch against the tree's pinned PG version fails the guard
   rather than silently re-pinning. This is the detector for Option A's one real
   cost.
6. UNITS + SMOKE green.

## References

- M0131 implementation plan §S8, `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
- `docs/design/0131-0007-ev-action-capture-tooling.md` (produces the manifest);
  `docs/design/0131-0006-system-view-relhasrules-flip.md` (S6.1/S6.2 interim blob patch)
- `postgres/src/include/access/transam.h:195-197`;
  `postgres/src/backend/catalog/system_views.sql`
- `internal/initdb/relcache_init.go:670-697`,
  `internal/initdb/pg_rewrite_bootstrap.go:15-70`,
  `internal/catalog/catalog.go:3604`

## Findings (implementation, 2026-08-11 — M0131-S8a)

Option A landed as recommended. Everything the design predicted about the
mechanism held; three things it left open are now settled by measurement.

**1. The oracle's assignment is deterministic — verified, not assumed.** Two
independent throwaway `initdb --no-sync` runs of PG 18.3
(`postgres/local_install/`) produced byte-identical assignments. Captured:

| view | view OID | rule OID | upstream reltype | relnatts |
|---|---|---|---|---|
| `pg_stat_replication` | 12231 | 12234 | 12233 | 20 |
| `pg_stat_wal_receiver` | 12240 | 12243 | 12242 | 15 |
| `pg_stat_recovery_prefetch` | 12244 | 12247 | 12246 | 10 |
| `pg_stat_subscription` | 12248 | 12251 | 12250 | 11 |
| `pg_replication_slots` | **12261** | 12264 | 12263 | 21 |
| `pg_stat_replication_slots` | 12266 | 12269 | 12268 | 10 |

`pg_replication_slots = 12261` confirms the design's deduction from the blob's
`eref` colnames exactly. Every `relnatts` matches goopg's nailed `RelNatts`, so
the repin is an OID change only — no shape change.

**2. The S6 landmine is disarmed by the policy itself, with no blob edit.**
S6.1 offered "fix the blob (12261 → 12105) or repin per S8". Repinning
`pg_replication_slots` to 12261 makes the verbatim
`pg_stat_replication_slots_ev_action.dat` correct as-captured, so **S6.1 is
discharged and should be struck rather than done** — there is no longer a blob
to patch, and S6.2's invariant guard is implemented here (guard 3) rather than
there. Proven non-vacuous: temporarily un-pinning the view to 12105 makes guard
3 fail on exactly that blob's two `:relid 12261` occurrences.

**3. The band ceiling, measured.** PG 18.3's initdb tops out at 13626 across
every OID-bearing catalog in a fresh template (max over
pg_class 13619 / pg_type 13621 / pg_rewrite 13622 / pg_proc 13626 /
pg_constraint 13301 / pg_namespace 13273). `goopgOnlySystemViewOIDBase = 15000`
is set from that number, leaving ~1.4k of headroom for PG minor-version growth
below it. The design asked for "a reserved disjoint sub-band" without a value;
this is it.

**4. Reordering risk, checked and clear.** The repin breaks the old
coincidence that declaration order equalled ascending-OID order (rule OIDs were
12101, 12107…12111; they are now 12234, 12243, …, with `pg_stat_wal_receiver`
declared first but no longer lowest). Every btree bootstrap in
`internal/initdb/btree_index_bootstrap.go` sorts explicitly before building the
leaf — all 20+ `bootstrap*Index` functions, including both pg_rewrite indexes
(`:1620`, `:1683`) and both pg_class indexes (`:939`, `:1085`) — so no index
leaf depends on declaration order. Had one not sorted, this change would have
produced a mis-ordered on-disk btree that a hosted PG binary-searches.

**5. The reload OID filter is unaffected.** `catalog_heap_reload.go`'s
`evClass < catalog.FirstUserOID` test still skips all six bootstrap rules
(12231..12266 < 16384); only its comment's OID range needed updating.

**6. `reltype` is now a *recorded* divergence, not an unknown.** S6.5 asked to
"probe, do not assume" whether upstream mints a per-view composite type: it
does (12233, 12242, 12246, 12250, 12263, 12268), where goopg's nailed rows all
carry 2249 RECORDOID. The captured values are in the pinned table and
`TestNailedViewRelTypeDivergenceIsDeliberate` asserts RECORDOID is still the
only divergence — adopting the composite OIDs requires real `pg_type` rows to
back them, which is left to S6.5/S9 with a ledger row. **S6.5's probe is
therefore answered; what remains under it is the implementation.**

### Guards as implemented

`internal/initdb/system_view_oid_pins_test.go`, 6 tests, all with
non-vacuity assertions:

| design guard | test |
|---|---|
| 1 nailed view OIDs match upstream | `TestNailedViewOIDsMatchUpstreamPins` (both directions: an unpinned nailed view is an error too) |
| 2 rule OIDs match upstream | `TestPgRewriteRuleOIDsMatchUpstreamPins` (reads what the bootstrap actually seeds) |
| 3 no unmapped in-band relid in any blob | `TestEvActionBlobsCarryNoUnmappedInBandRelid` |
| 4 disjointness + in-band | `TestSystemViewOIDPinsAreDisjointAndInBand` (also re-checks `catalog.FirstUserOID == FirstNormalObjectId`, the policy's premise) |
| 5 version stamp | `TestSystemViewOIDPinsOracleStampMatchesTree` (`server_version` BootVal + `config.CatalogVersionNo`) |
| — constants vs table | `TestSystemViewOIDPinConstantsAgree` |

Guard 1's acceptance measurement is additionally taken **against a live PG**, in
`assertSystemViewOIDsArePinnedToUpstream`
(`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`): a real PG 18.3
hosted on a goopg catalog resolves all six `pg_catalog.<view>::regclass::oid`
to upstream's own values. The unit guards compare goopg's tables to a table;
this compares goopg's on-disk bytes to a live PG's name→OID resolution. It is
`::regclass` rather than `SELECT * FROM <view>` because `relhasrules` is still
`'f'` — evaluation is S6.

### Scope limit (ledgered)

The repin, like S6's flip, reaches only freshly `goopg init`'d directories:
these OIDs are written by `bootstrapPgClassTuples` at initdb time. Every
existing goopg `$PGDATA` keeps 12100/12102..12106 on disk. There is **no
in-place upgrade path** — and unlike a `relhasrules` flag flip, a stale
directory now also disagrees with the committed `ev_action` corpus, so the
`pg_stat_replication_slots` blob on such a directory still names a
non-existent 12261. Re-initdb is required. Every M0131 gate initdbs fresh, so
nothing catches this automatically.

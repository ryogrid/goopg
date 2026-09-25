# R126 SCOPE (rev 3, APPROVED-WITH-NOTES) — persist FOREIGN KEY constraints across restart

Step (b) of R125's chain and the blocker for (d)/Q9.

**Rev 1 was BLOCKed on four disqualifying findings; rev 2 was BLOCKed on
three more.** Every finding was verified against source before being
accepted. Rev 1 described a third of the write surface, pointed the
reload at a database the writer never populates, and imported a
varlena-classification regression none of its gates could see. Rev 2
fixed those but asserted a *"verified"* four-site write surface that was
still missing three mutators, chose a routing that contradicted its own
mandatory-ALTER-form requirement, and claimed a codec primitive was
needed that already exists.

Rev 3's changes are contained: **the write surface collapses to the
existing `syncConstraintCatalogRow` funnel** rather than being
enumerated (§3), the reload mains at `DefaultDBOid` following
`pg_attrdef` (§5), the decode arm is handed the element-type name (§4),
and P0 asserts `conbin = NULL` so it cannot be masked (§7).

§9 keeps every error from both revisions, because most were *my own
positive claims about the code* rather than omissions — including two
where I quoted a precedent while doing the opposite of what it does.

## 1. The defect, measured

**Committed measurement** (`fk-loss-across-restart.md`, 2026-09-14 at
HEAD `14a11488f`), discharging R125 §7's flag that this was prose-only:
create an FK → `pg_constraint` shows it → CHECKPOINT → stop → start →
**0 rows**, data intact (500/500). Reproduced in a `CREATE DATABASE`d
database too — the P1b case.

**The measurement found more than R125 reported.** The loss is not
confined to planner evidence:

```
INSERT INTO child VALUES (99999, 99999, 'orphan');   -- no such parent
INSERT 0 1
```

**Referential integrity is silently unenforced after a restart.** Runtime
enforcement reads the same `catalog.Table.ForeignKeys`
(`operators_fk.go:118`, `:170`) that nothing repopulates. This round is a
**correctness** fix that happens to unblock Q9. PK uniqueness survives
(it rides the index): `INSERT INTO parent VALUES (1,…)` still raises
duplicate-key, and `parent_pkey`/`child_pkey` both reload.

**Separate gap, explicitly NOT this round:** `pg_constraint` returns 0
rows of *any* contype post-restart, including the `'p'`/`'u'` rows the
view synthesises from indexes (`catalog.go:7143-7153`) even though those
indexes survived. A second reload gap suppresses the PK projection. Do
not conflate it with the FK gap, and do not let P1 depend on it.

### The two halves

- **Write:** `syncTableToCatalogHeap` (`operators_ddl.go:18575`) streams
  pg_class, pg_attribute, pg_attrdef, pg_inherits, pg_rewrite — **no
  `contype='f'` row**.
- **Read:** startup scans 2606 at `catalog_heap_reload.go:1338`, but
  returns `errSkipBuiltinRow` for `contypid < FirstUserOID`, so an FK row
  (`contypid = 0`) could **never** survive it even if one existed.

## 2. Precedent

`pg_attrdef` (2604, `loadColumnDefaultsFromHeap`, `open.go:1638-1646`)
and `pg_inherits` (2611, `loadInheritanceFromHeap`, `:1650-1658`): write
real heap rows at DDL time + a standalone unconditional startup pass.
FKs need **pg_inherits' reasoning** — an FK is an edge, and the
referenced table may register after the referencing one.

The M0114 catalog cache does **not** bypass this: it stores no
`ForeignKeys` (`catalog_cache.go:67-90`), so a standalone pass is
genuinely required and P1 is not silently short-circuited by a cache hit.

`sys_pg_constraint.go` is the row-writing template
(`PGConstraintColumnsPG18`, `buildPGConstraintRowForDomainCheck`,
`writeHeapRowCanonical`). Its header names this round's residual —
**"Table constraints stay registry-only until B3"** — and `02d §2` scopes
pg_constraint into B3. This is a planned conversion, taken one slice
early; record that in the B3 TODO.

## 3. SEVEN mutators, and the answer is a funnel, not seven emits (BLOCK findings 1 & 2; rev 2 findings)

Rev 1's entire write half was "emit rows in `syncTableToCatalogHeap`",
covering only `CREATE TABLE` (`:3866`, `:3899`). **Rev 2 then asserted a
"verified" four-site surface and was still wrong — three more mutators
change `catalog.ForeignKey` with zero heap work**, and two of them carry
exactly the fields this chain exists to preserve:

| site | today | why it matters |
|---|---|---|
| `CREATE TABLE … REFERENCES` | flows into `syncTableToCatalogHeap` | — |
| **`ALTER TABLE … ADD FOREIGN KEY`** (`:9032-9125`) | ends at `tbl.ForeignKeys = append(…)` (`:9125`), **no sync** | the form HammerDB and step (d) use |
| **`ALTER TABLE … DROP CONSTRAINT`** FK arm (`:13238-13244`) | `im.DropForeignKeyConstraint(…)`, `return nil` | zero heap work |
| **`deleteCatalogRowsForOID`** (`:18095-18180`) | stamps 1259/1249/2604/2611/2618 — **2606 absent** | without it every re-sync **duplicates** the FK row |
| **`execAlterTableAlterConstraint`** (`:13436-13481`) | sets `Deferrable`, `InitiallyDeferred`, **`NotEnforced`**, **`NotValid`**, `return nil` | **this is the statement that sets `conenforced`** — the field P2 says would re-break R125 if lost |
| **`AlterTableValidateConstraint`** (`:9126-9175`) | `NotValid = false`, no heap work | a VALIDATEd FK silently reverts to NOT VALID across restart |
| **`AlterTableRenameConstraint`** FK arm (`:10147`+) | conname in memory only | heap `conname` goes stale; OID-keyed delete still works but row and registry disagree |

**Decision: stop enumerating emit sites; route every FK mutator through
the existing funnel.** `syncConstraintCatalogRow` (`:12859-12872`)
already does delete-then-re-derive: it stamps via
`deleteCatalogRowsForOID` for **every** `tableCatalogDBOids(ctx)` and
then calls `syncTableToCatalogHeap`, which carries both the per-DB
routing and the mirror. So once 2606 is added to
`deleteCatalogRowsForOID` and FK emission is added to
`syncTableToCatalogHeap`, **one `syncConstraintCatalogRow` call from each
FK-mutating ALTER covers ADD / DROP / ALTER CONSTRAINT / VALIDATE /
RENAME uniformly** — no per-site transcription, and no exposure to the
"sibling paths must agree" hazard this repo keeps paying for.

### The 2606 stamp predicate — the load-bearing detail

**Decode via the descriptor; key on `conrelid` AND `contype='f'`.**

- **Not a hand-computed offset.** The existing arms will tempt the
  implementer the wrong way: pg_attrdef stamps `data[4:8]`
  (`:18147-18149`) and pg_inherits `data[0:4]` (`:18161-18163`), raw
  fixed offsets that work because those columns sit at the front. In a
  pg_constraint row `conrelid` is the **9th** column, behind a 64-byte
  `name`, so the offset is ~80 and depends on the `name` fixed-width
  normalisation — right once, wrong after any layout touch. Follow the
  pg_class/pg_attribute arms instead (`DecodePGClassPhysicalRow`,
  `:18104`): decode with `PGConstraintColumnsPG18()` and compare
  `decoded[8]`.
- **The `contype='f'` half prevents a FUTURE silent corruption.** Today
  domain rows carry `conrelid = 0`, so a conrelid-keyed stamp cannot
  reach them. But **B3 adds `contype='c'` table CHECK rows with a real
  `conrelid`**, and this funnel re-emits only FK rows — so a
  conrelid-only predicate would silently delete every table CHECK row on
  the next ALTER the moment B3 lands. Guard on `contype` now, and record
  it in the B3 TODO note §2 already promises.

### VALIDATE CONSTRAINT is the one deliberate exception to the funnel

`AlterTableValidateConstraint` takes `lmgr.ShareUpdateExclusiveLock`
(`:9137`) on purpose, citing PG's `AlterTableGetLockLevel` and noting it
does **not** conflict with concurrent INSERT/UPDATE/DELETE. Every other
`syncConstraintCatalogRow` caller runs under a stronger ALTER lock.
Routing VALIDATE through the full funnel would delete and rewrite the
table's entire pg_class/pg_attribute/attrdef/inherits/rewrite row set —
plus the index entries `syncTableToCatalogHeap` re-inserts
(`:18613-18620`, `:18630-18634`) — **while concurrent DML proceeds**.

**Decision: VALIDATE stamps and re-emits only its own 2606 row**, not the
full funnel. Preserving PG's lock semantics outweighs uniformity here,
and the narrower write is sound precisely because `convalidated` is the
only field it changes. This is the one place "one funnel everywhere"
costs something; it is taken consciously rather than by omission.

### Implementation checks

- Any cascade path reaching `im.DropForeignKeyConstraint`
  (`catalog.go:22168`) other than `:13241`, and partition ATTACH.
- **Six `deleteCatalogRowsForOID` call sites hardcode
  `catalog.DefaultDBOid`** (`:24974`, `:25014`, `:25057`, `:25104`,
  `:25349`, `:25401`) instead of looping `tableCatalogDBOids`. They look
  domain/composite-scoped, but once 2606 is in the function they become
  FK-stamping sites with fixed routing. **Confirm none can reach a
  per-DB table.**
- **Free win, name it so nobody "fixes" it later:** `operators_tx.go:376`
  also calls `deleteCatalogRowsForOID`, so a rolled-back CREATE TABLE's
  FK row is cleaned up automatically once 2606 is in the set.

**Why finding 1 was fatal on its own:** HammerDB's TPC-H load and step
(d) declare the eight FKs as `ALTER TABLE … ADD CONSTRAINT … FOREIGN
KEY` (R125 SCOPE §1.4). A P1 pin written with `CREATE TABLE t (…
REFERENCES p)` passes green while **the form that matters persists
nothing**. **P1/P1b must use the ALTER form**; a CREATE-form pin is
added alongside, not instead.

**Why finding 2 was fatal on its own:** rev 1 claimed
"`syncConstraintCatalogRow` … `deleteConstraintRowByOID` … already do the
dance". Both citations were wrong — `syncConstraintCatalogRow` calls
`deleteCatalogRowsForOID`, and `deleteConstraintRowByOID`'s only three
call sites (`:25628`, `:25753`, `:26105`) are ALTER/DROP **DOMAIN**.

Nothing table-side reaches `deleteConstraintRowByOID`, so without the
2606 stamping the round produces N duplicate `ForeignKeys` entries per
ALTER — and P3, which tested DROP only, would not have caught it.

## 4. Codec: take the KindBytes path, NOT the descriptor swap (BLOCK finding 4 & 5)

Rev 1 proposed redeclaring `conkey`/`confkey` as
`{Name:"int2", IsArray:true}`. **That would corrupt the catalog**, and
none of rev 1's gates could see it:

`catalog.PhysicalTypeIsVarlena` (`physical_align.go:85-107`) switches on
`t.Name` **only, with no `IsArray` arm** — verified. So
`{Name:"int2[]"}` → default → `true`, but `{Name:"int2", IsArray:true}` →
the `"int2"` case → **`false`**. It feeds `pgRowHasVarWidth`
(`codec.go:1573-1590`) → `HEAP_HASVARWIDTH`. On an FK row PG stores
`conbin` NULL, so a non-null `conkey` is the tuple's only varlena and the
bit would be **unset** — exactly the failure `codec.go:1550-1557` warns
about by name (PG18 `nocachegetattr`, `heaptuple.c:642`
`Assert(j > attnum)`), and `relcache_init.go:2895-2900` hands a real
standby precisely that TupleDesc (`conkey` TypeOID 1005, len -1).
goopg's own encode and decode both branch on `IsArray`, so they stay
symmetric and a round-trip pin passes while the infomask is wrong.

**Rev 1 also overstated the encode defect.** `case "int2[]", "_int2"`
(`codec.go:962-966`) has a `KindBytes` passthrough — the mechanism the
pg_proc seeder already uses to write real `oid[]`/`text[]` arrays. So
encode works today for a pre-built `ArrayType` blob. The genuine gap is
**decode only**: no `int2[]`/`oid[]` case in the scalar switch, so a
non-null value lands in the varlena-text default (`codec.go:2087`).

**Decision: keep the `int2[]`/`oid[]` type names; write pre-built
`ArrayType` blobs via `KindBytes`; add `int2[]` and `oid[]` decode arms.**
Byte-inert, no varlena reclassification, and it reuses the seeder's
proven pattern. `array.ElemTypeInfo` supports both `int2` (21) and `oid`
(26) (`pgarray.go:155-162`).

**No new builder is needed — rev 2 claimed one was, and that was wrong.**
`encodeArrayValuePGCtx` (`codec_array.go:61-110`) already builds the full
non-empty blob from `Type{Name:"int2", IsArray:true}` plus a `"{1,2}"`
`KindString`, **including** the `construct_md_array` trailing-pad
fidelity fix (`:97-101`) that a hand-rolled builder would omit and that
`pg_column_size` and pg_amcheck can see. The write half is therefore: build
the blob with that existing call, hand it to the row as a `KindBytes`
datum, and let the `int2[]` passthrough splice it. One call, not a second
transcription of the 24-byte layout.

**Decode trap the cut must name.** `decodeArrayValuePGStyled` renders via
`array.RenderTextStyled(t.Name, …)` (`codec_array.go:377`), which
resolves the **element** type by name with a silent `!ok` fallback to
varlena-text elements (`pgarray.go:277-280`). So a naive
`case "int2[]": return decodeArrayValuePGStyled(*t, …)` passes
`elemName="int2[]"`, misses, and decodes 2-byte ints as 4-byte varlena
text — **garbage, no error**. The arm must construct
`catalog.Type{Name:"int2", IsArray:true}` (element name) before calling.
Same for `oid[]`. P0 must be written so this failure is visible.

**Recorded, NOT fixed here:** `PhysicalTypeIsVarlena`'s missing `IsArray`
arm is a latent bug for ordinary user `int4[]` columns too. Out of scope;
it deserves its own round and a deferral-ledger row.

## 5. Routing and reload (BLOCK finding 3)

Rev 1 prescribed a main reload pass over `cat.DBOID()`. **Both cited
siblings do the opposite**, and one carries the scar tissue:
`loadColumnDefaultsFromHeap` mains at `catalog.DefaultDBOid`
(`catalog_heap_reload.go:222`), and `loadStatisticsFromHeap`
(`open.go:3841-3854`) documents that reading `cat.DBOID()` "meant the
default database's stats reload has been DEAD in practice since M0112 …
pg_class survives that split only because DDL mirrors its pages to
base/5; pg_statistic was never mirrored".

**`2606` is absent from `mirroredCatalogOIDs()` entirely** — verified,
the OID does not appear anywhere in
`sys_catalog_postgres_db_mirror.go`. pg_constraint reaches base/5 only
via the explicit `mirrorConstraintCatalogFiles()` calls on the domain
paths, which an FK write would not hit. A `cat.DBOID()`-rooted main pass
would therefore find nothing — the same silent no-op the mirror set's own
pg_index comment describes.

**Rev 2 got the conclusion backwards while quoting the refuting
precedent.** It proposed "main at `cat.DBOID()`, justified by adding 2606
to the mirror set". But **`pg_attrdef` is already in the mirror set**
(`sys_catalog_postgres_db_mirror.go:203`) and
`loadColumnDefaultsFromHeap` **still mains at `catalog.DefaultDBOid`**
(`catalog_heap_reload.go:222`). "Mirrored ⇒ safe to main at
`cat.DBOID()`" is not the repo's pattern; the mirrored catalog with the
closest shape does the opposite.

Worse, rev 2's routing contradicted its own §3.
`mirrorTouchedCatalogsToPostgresDB` has exactly one caller on this
surface — `syncTableToCatalogHeap`, and only when
`heapDBOid == catalog.DefaultDBOid` (`operators_ddl.go:18708-18713`).
None of rev 2's bespoke write sites went through it, so an FK added by
the **mandatory ALTER form** would write `base/1/2606`, never mirror, and
a `cat.DBOID()` main pass reading `base/5/2606` would find nothing. The
round would have failed its own headline pin.

**Decision:**
1. Write FK rows through the §3 funnel, so routing is
   `tableCatalogHeapDBOid(ctx)` (`:18460`) — the table's own pg_class
   routing — and the mirror fires exactly where it fires for tables.
   TPC-H's tables live in database `tpch`; getting this wrong makes step
   (d) measure nothing.
2. Reload **main pass at `catalog.DefaultDBOid`**, following
   `loadColumnDefaultsFromHeap` exactly, then the
   `for _, dbName := range cat.ListDatabases()` loop with the same skip
   set (`open.go:1563-1571`, `:3856-3859`). **The reload does not depend
   on the mirror.**
3. Add 2606 to `mirroredCatalogOIDs()` for **PG-standby readability (P5)
   only** — never as a reload dependency.

**Double-mirror checked rather than assumed: harmless.**
`mirrorCatalogRelToPostgresDB` is a page-by-page base/1 → base/5 copy
with a `bytes.Equal` skip (`sys_catalog_postgres_db_mirror.go:115-127`),
and `mirrorConstraintCatalogFiles` (`sys_pg_constraint.go:138-140`) is
the same call on the same OID — a second pass sees every page equal and
copies nothing. base/1 is already the master for domain CHECK rows
(`pgConstraintRel` pins `DefaultDBOid`), so the domain reload at
`cat.DBOID()` keeps reading identical content. **Consequence to record:**
base/5/2606 is now wholesale-overwritten from base/1 on every table DDL,
so nothing may ever write a pg_constraint row directly to base/5 —
nothing does today, and a successor must not start.

**State plainly:** the FK pass and the domain pass read *different*
databases by design (`DefaultDBOid` vs `cat.DBOID()`).

Placement: a standalone unconditional pass **after**
`loadInheritanceFromHeap`, per §2's load-order argument.

**OID counter (finding 9).** Startup advances past OIDs from
`cat.AllTables()` only (`open.go:1501-1506`) — table OIDs, not constraint
OIDs. A restored FK OID could therefore be re-issued to a new object. The
reload must `AdvanceNextOIDPast` every FK OID it restores. Pinned as P6.

## 6. Reaching the planner (finding 7)

`keysCovering` reads `info.table.ForeignKeys` directly
(`joinrelsize.go:657`) and `columnsSubset` (`:690`) matches on column
**names**, while `fkParentRel` (`:760`) matches the parent with
`tbl.Name != fk.RefTable` — **case-sensitive, exact**. So the reload must
map `conkey` attnums back to column names and `confrelid` to the table's
canonical `Name` with matching case. A reload that gets this subtly wrong
repopulates `ForeignKeys`, makes `pg_constraint` render, and leaves the
planner arm **silently dead**. That is what P1 is for. Nothing else is
needed — R125 already relaxed the consumption guard.

Reference resolution: store `confrelid` as an OID (PG's semantics) and
resolve OID → the table's *current* name on reload. That is deliberately
not a name round-trip, and the payoff is concrete rather than
theoretical: **renaming the parent table leaves the child's in-memory
`fk.RefTable` stale today** — a pre-existing `fkParentRel` bug — and the
OID-keyed reload **repairs it at the next restart**. So §6's OID choice
is not gratuitous. A missing parent drops
the FK with a logged warning rather than restoring it dangling.

`RefColumns` empty ("use parent PK", `catalog.go:1665`) is preserved
**verbatim**, not materialised — materialising would change
`pg_get_constraintdef` output. (PG always stores a concrete `confkey`;
that divergence is pre-existing and stays.)

## 7. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | **Non-null `conkey`/`confkey` round-trip AND the tuple's infomask is right**: a written FK row has `HEAP_HASVARWIDTH` set, and `conkey` decodes back to the same attnums for **both a multi-column and a single-column** key. Mutation-tested. **The row under test must assert `conbin = NullDatum`** — see the mask below. *(Replaces rev 1's P0, which was known-true by construction — §9)* | wrong infomask ⇒ the codec decision is wrong; empty `conkey` ⇒ the `emptyArrayTypeBytes` trap; text-looking attnums ⇒ the §4 element-name trap |
| P0b | Cheap regression guard, **not** a test of the change: existing pg_constraint heap rows stay byte-identical on a fresh initdb (`base/{1,5}/2606` md5) | movement ⇒ STOP |
| P1 | **An FK declared via `ALTER TABLE … ADD CONSTRAINT` survives a restart and reaches `superkeyJoinSelectivity`** — assert the join estimate, not the synthesised view, and cover §6's name/case mapping | view back but estimate unchanged ⇒ the planner arm is dead |
| P1b | Same, in a non-`postgres` database | works in `postgres` only ⇒ routing wrong ⇒ step (d) is a no-op |
| P1c | **Enforcement returns**: today's succeeding orphan INSERT must fail with a foreign-key violation after a restart | row back but orphan accepted ⇒ the reload fed the view, not the store |
| P2 | Per-field round-trip: `conkey`/`confkey`/`confupdtype`/`confdeltype`/`confmatchtype`/`condeferrable`/`condeferred`/`convalidated`/`conenforced`/`confdelsetcols`/`OID`/`Name`. Mutation-test `conenforced` and `NotValid`. **`condeferred` must use the CREATE form** — the ALTER builder (`:9101-9114`) never sets `InitiallyDeferred`, so an ALTER-form pin cannot distinguish "lost in the heap" from "never set" (finding 10) | a silently-defaulted `conenforced` would re-break R125 |
| P3 | DROP CONSTRAINT and DROP TABLE remove the row; **and N repeated ALTERs leave exactly one** `ForeignKeys` entry after restart (the finding-2 duplication check) | duplicates ⇒ the 2606 stamping is missing |
| P4 | Values unchanged: TPC-H spotcheck AND TPC-DS SF0.25 sweep, **both run**. A DDL write-path change gets no judgement | ANY movement ⇒ STOP |
| P5 | **`pg_amcheck` on 2606 passes after an FK is written.** Given a mechanism rather than left as a bound: this is the first non-null varlena **array** goopg will ever have written into a system catalog, so a heap-structure check is the cheap, targeted probe. (The infomask half of the standby risk is already carried by P0, which is the sharp one; a full standby attach is not required) | amcheck failure ⇒ the blob or the infomask is malformed |
| P6 | A restored FK OID is not re-issued: `AdvanceNextOIDPast` covers reloaded constraint OIDs. **Belt-and-braces, not a demonstrated live bug** — the pg_control checkpoint advance runs earlier (`open.go:1499`) and may already cover it; do not call it corruption without showing that path can miss a 2606-only OID | collision ⇒ a new object silently reuses an FK's OID |

**The P0 mask, stated so the pin cannot be written around it:** if the FK
builder writes `NewStringDatum("")` for `conbin` — copying
`buildPGConstraintRowForDomainCheck`'s empty-string convention
(`sys_pg_constraint.go:91-93`), which §8's own hazard paragraph puts
front of mind — that empty text is a **non-null varlena**, so
`HEAP_HASVARWIDTH` gets set unconditionally and P0 passes whatever the
descriptor says. PG stores `conbin` NULL for an FK. On a correct FK row
`conkey` is the tuple's *only* varlena (`conname` is `name` → false,
`physical_align.go:100`; the four `char` columns have no Args → false,
`:87-90`; everything else is bool/oid/int2), which is exactly what makes
P0 sharp.

**No parity prediction.** This round declares no FK on the bench corpus:
TPC-H stays 6/22, TPC-DS 2/99. Its value is that step (d) becomes
possible — and that referential integrity stops silently lapsing.

## 8. Bounds and hazards

- **No planner change** (R125 did the consumption side); **no corpus
  change** (that is step (d)); **FK validation performance out of scope**
  (step (c), still optional since NOT VALID FKs declare in O(1)).
- **TPC-DS is unaffected either way** — its base DDL declares 24 PKs and
  **zero** FKs, and `tools/tpcds_ri.sql` is referenced by nothing. The
  whole FK chain is TPC-H-only (R125 addendum, verified).
- **No pg_constraint index maintenance.** Correction to rev 1, which
  claimed 2664-2667 "stay bootstrap-empty" (finding 6): M0133-S1 **does**
  populate 2665/2666/2667 at initdb
  (`initdb.go:1360-1372` → `pg_constraint_bootstrap.go:96-219`); 2664 is
  not written at all. The real residual is **no runtime index
  maintenance**, so a standby reading via
  `pg_constraint_conrelid_contypid_conname_index` sees no FK. P5 claims
  only "not regressed".
- The two existing writers **disagree** on the non-FK char columns —
  bootstrap writes `" "`, the runtime domain writer `""`. FK rows carry
  real values, so this round is unaffected; do not copy the empty-string
  convention into a column PG defines as a non-space char.
- `conbin` is raw text, not a real `pg_node_tree`, in the runtime writer.
  FKs do not use `conbin` — and an FK row must write it **NULL**, not
  `""` (see the P0 mask in §7).
- **`conpfeqop`/`conppeqop`/`conffeqop` stay NULL** where PG populates
  them for every FK (and `conexclop` stays NULL, correctly). Harmless for
  goopg's own planner and enforcement, but it is a **standby-visible
  divergence**: recorded here, not fixed, and P5 claims only "not
  regressed".

## 9. What rev 1 and rev 2 got wrong (kept, not deleted)

**Rev 2's own three defects, all of the same class as rev 1's:**

A. **"SEVEN mutators, not four" — rev 2 asserted a *verified* four-site
   surface and missed three**, two of which carry `conenforced` /
   `convalidated`, the exact fields the chain exists to preserve (§3).
   The lesson taken: enumeration was the wrong technique; the funnel is
   the fix.
B. **The routing decision contradicted rev 2's own mandatory-ALTER-form
   requirement**, and quoted `pg_attrdef` as precedent while doing the
   opposite of what `pg_attrdef` does (§5).
C. **"A non-empty int2 ArrayType builder is needed" was false** —
   `encodeArrayValuePGCtx` already builds it, with a `construct_md_array`
   trailing-pad fidelity fix a hand-rolled builder would have dropped.
   Rev 2 would have written a second, worse transcription of the layout
   (§4). It also missed that the decode arm silently decodes ints as
   text unless handed the element-type name.

**Rev 1's six:**

Two of these were positive claims I made about the code, which matters
more than the omissions:

1. **"`syncConstraintCatalogRow` / `deleteConstraintRowByOID` already do
   the delete-then-resync dance"** — both citations wrong; neither is
   reachable from any table-side FK path (§3).
2. **"Redeclaring the columns `{int2, IsArray}` is the tested path and is
   byte-inert"** — byte-inert for existing rows, but it silently flips
   `PhysicalTypeIsVarlena` and corrupts the infomask of any *new* row
   with a non-null array (§4). The safer fix was available and rev 1
   dismissed it on a mis-stated premise ("would silently write an EMPTY
   array" omitted the `KindBytes` passthrough I had read).
3. Reload main pass at `cat.DBOID()` — contradicted by both siblings and
   by `loadStatisticsFromHeap`'s documented scar (§5).
4. Write surface described as one site when it is four (§3).
5. P0 was known-true by construction: encode skips NULLs before
   alignment (`codec.go:1591-1594`) and decode skips them on the bitmap
   (`:1451-1455`), and every array value in both writers is `NullDatum`.
   An md5 that cannot move is not a measurement (§7 P0/P0b).
6. "2664-2667 stay bootstrap-empty" — copied from a stale in-code
   comment without checking (§8).

## 10. Gates (FOREGROUND)

1. Suites green (`internal/executor`, `internal/initdb`,
   `internal/catalog`, `internal/optimizer`); `go vet`.
2. Pins for P0/P0b/P1/P1b/P1c/P2/P3/P6, with P1 in the **ALTER** form.
3. **Values: TPC-H spotcheck AND TPC-DS SF0.25 sweep, both run** (P4).
4. Parity capture both corpora to confirm TPC-H 6/22 and TPC-DS 2/99 —
   TPC-H via `estimate-audit -plan-only` (**never** `capture-tpch.sh`:
   per-connection ANALYZE stats make it capture empty-stats plans),
   TPC-DS via `capture-tpcds.sh`, `work_mem` pinned.
5. `make plan-gate`: run, or record as a reasoned omission.
6. R125 §7's second prose-only figure (32.17 s validation) is **not**
   re-measured here — this round declares no FK and runs no validation.
   Step (c)'s scope inherits the obligation; say so rather than let it
   lapse.
7. REPORT.md → agent review → `commit -n` + push.

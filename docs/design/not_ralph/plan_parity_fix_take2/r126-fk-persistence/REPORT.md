# R126 result — foreign keys survive a restart, and so does enforcement

Step (b) of R125's chain. **No plan moves and none was predicted**: TPC-H
stays 6/22, TPC-DS 2/99. What changes is that step (d) becomes possible
at all — and that a silent correctness bug is gone.

## 1. What was broken

Measured before the scope was reviewed (`fk-loss-across-restart.md`,
HEAD `14a11488f`), discharging R125 §7's flag that this figure was
prose-only. Create an FK → CHECKPOINT → stop → start → `pg_constraint`
**0 rows**, data intact, in `postgres` *and* in a `CREATE DATABASE`d
database.

The measurement found more than R125 reported:

```
INSERT INTO child VALUES (99999, 99999, 'orphan');   -- no such parent
INSERT 0 1
```

**Referential integrity was silently unenforced after a restart** —
runtime enforcement reads the same `catalog.Table.ForeignKeys`
(`operators_fk.go:118`, `:170`) that nothing repopulated. So this is a
correctness fix that happens to unblock Q9, not a planner nicety.

Two halves, both confirmed by source read: `syncTableToCatalogHeap`
emitted no `contype='f'` row, and the startup scan of 2606
(`catalog_heap_reload.go:1338`) returns `errSkipBuiltinRow` for
`contypid < FirstUserOID`, so an FK row (`contypid = 0`) could never
have survived it even if one had existed.

## 2. The cut

| piece | where |
|---|---|
| FK row builder + writer, per-DB routing | `sys_pg_constraint.go` (`buildPGConstraintRowForForeignKey`, `writeForeignKeyConstraintRow`, `pgConstraintTableRel`, `int2ArrayDatum`, `resolveFKCatalogKeys`) |
| Emit from the DDL funnel | `operators_ddl.go` `syncTableToCatalogHeap` |
| 2606 stamping | `stampForeignKeyConstraintRows`, called from `deleteCatalogRowsForOID` |
| Mutators routed through the funnel | ALTER ADD FK, DROP CONSTRAINT, ALTER CONSTRAINT, RENAME CONSTRAINT |
| VALIDATE's narrow write | `resyncForeignKeyCatalogRow` |
| Reload | `loadForeignKeysFromHeap` + `open.go` pass after `loadInheritanceFromHeap` |
| Standby readability | 2606 added to `mirroredCatalogOIDs` |
| Codec | `int2[]`/`oid[]` decode arms (`codec.go`); `FKActionChar`/`FKActionFromChar` (`catalog.go`) |
| Tuple predicate plumbing | `stampCatalogRowsTuple` — the data-only form cannot decode a NULL-bearing row |

Three decisions worth stating because the reviews reversed me on each:

- **The write surface is a funnel, not an enumeration.** Seven sites
  mutate `catalog.ForeignKey`; rather than emit from each,
  `syncConstraintCatalogRow` (which already stamps per-DB then re-syncs)
  is called from each mutator. One code path covers ADD / DROP / ALTER
  CONSTRAINT / RENAME.
- **VALIDATE CONSTRAINT is the one deliberate exception.** It holds only
  `ShareUpdateExclusiveLock` and so runs alongside concurrent DML; the
  full funnel would delete and rewrite the table's entire catalog row
  set and re-insert index entries under that weak lock. It re-emits just
  its own row, which is sound because `convalidated` is the only field
  it changes.
- **The columns keep their `int2[]`/`oid[]` names.** Redeclaring them
  `{Name:"int2", IsArray:true}` — rev 1's plan — would have corrupted the
  catalog; §4 below.

## 3. Results

| # | bar | result | verdict |
|---|---|---|---|
| P0 | non-null `conkey` round-trips AND `HEAP_HASVARWIDTH` is set | unit pins green, **both mutation-tested** (§4). Confirmed **on disk**: every live FK row in `base/1/2606` carries `HASVARWIDTH=true`, `HASNULL=true`, natts=28 | **PASS** |
| P0b | existing pg_constraint rows byte-identical | `base/1/2606` and `base/5/2606` md5 `50f49d12…` on both R125 and R126 initdb; indexes 2665/2666/2667 identical too | **PASS** |
| P1 | FK survives restart, **declared via ALTER** | **automated pin** `TestForeignKeySurvivesRestart` — asserts `ForeignKeys` (the field, not the synthesised view), incl. Columns/RefColumns/RefTable/OnDelete/OID. **Mutation-tested twice**: disabling the reload pass, and removing the ALTER-ADD sync alone, each give `ForeignKeys=0`. Also observed live: `conkey={2} confkey={1} confdeltype=c` | **PASS** |
| P1b | same in a non-`postgres` database | live: `dchild_pid_fkey` back in `fkdb` with `convalidated=f` — **NOT VALID preserved**, what R125 made load-bearing. **Manual, NOT pinned** — see the note below; a schema is not a database, so the non-public-schema pin does not cover this | **PASS (manual)** |
| P1c | enforcement returns | live: the orphan INSERT that succeeded before now raises `23503` with PG's DETAIL. **Manual, not pinned** — the in-process harness runs DDL, not DML | **PASS (manual)** |
| P2 | per-field round-trip | unit pins per case, `conenforced` mutation-tested; `condeferred` and `convalidated` now also pinned **across a real restart** (`…RepeatedAlterDoesNotDuplicate`, `…ValidateSurvivesRestart`) | **PASS** |
| P3 | DROP removes it; N ALTERs do not duplicate | **automated pins** `TestForeignKeyDropDoesNotResurrect` and `…RepeatedAlterDoesNotDuplicate` (4 ALTERs incl. RENAME → exactly 1 FK, new name, Deferrable preserved). Live: 5 stamped tuples / 2 live on disk | **PASS** |
| P4 | values unchanged | TPC-H spotcheck **RESULT=PASS** (Q12=2, Q13=34), re-run after the block fixes. TPC-DS SF0.25 **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3**, plan-shape **99/99 same, 0 changed** | **PASS** |
| P4b | **TPC-H parity unchanged at 6/22 — measured, not inherited** | `estimate-audit -plan-only` + `pg-plan-parity-diff.py`: `queries=22 match=6 shapediff=14 unparsed=0 missingnode=2 error=0`. Artefact committed as `r126-tpch.plans.txt` | **PASS** |
| P5 | standby readability not regressed | **scope's mechanism unavailable** — see §5 | **PARTIAL** |
| P6 | restored FK OID not re-issued | `AdvanceNextOIDPast` called per reloaded FK OID — **implemented, not exercised** (no collision case constructed) | **UNTESTED** |
| gate 5 | `make plan-gate` | **FAILS: 20/22 diverged — and fails identically on the R125 binary**, measured by re-running it against `goopg-r125` on the same data dir. It is a goopg-vs-PG plan diff, so it cannot pass until the goal itself is reached; not an R126 regression | **pre-existing FAIL** |

**Seven restart pins were added after review** (`internal/initdb/fk_restart_test.go`),
and they are the round's most important artefact. The bug existed because
**nothing in the suite crossed an `Open→Close→Open` boundary with an FK
declared** — a round that fixes untested-restart-path rot must not close
without closing that hole. Every case uses the **ALTER** form, so a
CREATE-only test could not have passed against the broken code.

**The gap that remains in the pins, named because it is the one that
matters most:** none of them crosses a **database** boundary, so
`pgConstraintTableRel`'s per-DB branch and the reload's `ListDatabases`
loop — the two paths TPC-H and step (d) actually depend on, and the two
that rev 1 and rev 2 each got wrong — are covered by **manual psql
evidence only**. I tried to add the pin and the harness cannot reach it:
`CREATE DATABASE` is a dispatch-layer statement that the in-process
parser rejects outright (`syntax error … expected TABLE, INDEX, VIEW …
after CREATE`), so an in-process version would have to hand-synthesise
the database registration and would then be pinning a parallel path
rather than the real one. Recorded as a follow-up rather than faked.

Suites: `internal/executor`, `internal/initdb`, `internal/catalog`,
`internal/optimizer` green; `go vet ./internal/...` clean. Two pre-existing failures are
**not** from this round and were verified as such by stashing the change:
`internal/parser` fails **60 tests both with and without** R126, and
`bak/` is an untracked directory that fails to build.

## 4. The two mutation tests, and why P0 needed to be two pins

Rev 1 proposed redeclaring `conkey`/`confkey` as
`{Name:"int2", IsArray:true}`. `PhysicalTypeIsVarlena`
(`physical_align.go:85-107`) switches on `Name` with **no `IsArray` arm**,
so that spelling reports int2 as fixed-width, leaves `HEAP_HASVARWIDTH`
unset on a row whose only varlena is a non-null `conkey`, and trips
PG18's `nocachegetattr` fast-path walker (`heaptuple.c:642`).

**Applying that mutation makes `TestPGConstraintFKRowHasVarWidthInfomask`
fail while `TestPGConstraintFKRowArrayRoundTrip` still passes** — measured,
not argued. That is the whole reason the infomask is a separate pin:
goopg's encode and decode both branch on `IsArray`, so they stay
symmetric under the wrong descriptor and a round-trip test is blind to it.

Second mutation: replacing `int2ArrayDatum`'s `encodeArrayValuePGCtx`
call with a plain text datum yields `conkey = "{}"` — the
`emptyArrayTypeBytes` trap. An empty `conkey` would make `keysCovering`
decline, costing the round its entire purpose, silently.

P0 also asserts `conbin` is NULL on the row under test: an empty string
there is a non-null varlena that would set the bit unconditionally and
mask the check.

## 5. What is NOT established

**P5 is PARTIAL and the scope's chosen mechanism did not exist.** The
scope specified `pg_amcheck` on 2606 — goopg has no `amcheck` extension
installed (`skipping database "postgres": amcheck is not installed`), so
that gate could not run. Substituted a direct on-disk read of the heap
(a throwaway probe over `base/1/2606`), which confirms
`HEAP_HASVARWIDTH` and `HEAP_HASNULL` on every live FK tuple — the
specific risk P5 existed for. **A real PG standby was not attached**, and
the pre-existing residual stands: no runtime pg_constraint index
maintenance, so a standby reading via
`pg_constraint_conrelid_contypid_conname_index` still sees no FK rows.

**P1's "reaches the estimator" half is established by composition, not
by an end-to-end estimate — and one leg of that composition is weaker
than the first draft claimed.** I tried three fixtures to get an EXPLAIN
whose row estimate moves when the FK is present — including a correlated
two-column join built to reproduce R125's 5x gap — and **in all three
goopg's baseline estimate was already exact (40,000 = ground truth), so
the FK arm had nothing to correct and the A/B could not move.** I am
reporting that as a limitation rather than dressing it up. What *is*
measured: the synthesised `pg_constraint` view iterates
`tbl.ForeignKeys` (`catalog.go:7248`), so the view rendering the FK is
direct evidence the field is repopulated; and enforcement reads the same
field (P1c). **Correction from review: P1c is NOT an independent witness
for the planner path.** Enforcement resolves the parent with
`im.LookupTable(...)` (`operators_fk.go:637`) — a normalising lookup —
while `fkParentRel` does a raw `tbl.Name != fk.RefTable` byte compare
(`joinrelsize.go:760`). A `RefTable` differing only in case would pass
P1c and still kill the planner arm. What actually closes that gap is
construction, now pinned: the reload assigns `RefTable` from the very
`*catalog.Table` the planner later compares against, so they are equal by
construction, and `TestForeignKeySurvivesRestart` asserts the exact
string. A related nuance worth recording: the reload **canonicalises**
`RefTable` from the as-typed DDL string to the catalog's name, so for a
quoted mixed-case parent the planner can behave differently before and
after a restart — post-restart being the correct one. `keysCovering` reads that same field
(`joinrelsize.go:657`), and R125 already mutation-pinned that an FK in it
reaches `superkeyJoinSelectivity`. The chain is sound but the last link
is an argument.

**P6 is implemented but untested.** The OID advance is belt-and-braces —
the pg_control checkpoint advance runs earlier — and I did not construct
a case that forces a collision.

**`make plan-gate` fails, pre-existing.** 20/22 diverged — and it fails
**identically on the R125 binary**, which I measured rather than assumed
by re-running the gate against `goopg-r125` on the same data dir. It
diffs goopg against live PG, so it cannot pass until this workstream's
goal is reached; it is not an R126 signal either way.

**Why the reviewer's proposed FK-sensitive fixture would still not have
worked, and what would.** `keysCovering` tries an **index** arm before
the FK arm (`joinrelsize.go:645-655`): any unique index whose columns are
a subset of the join columns already yields the exact clamp. Every
fixture I built gave the parent a `PRIMARY KEY` on the join columns, so
the index arm fired and the FK arm was redundant — that is *why* the
baseline was already exact, and it is a better explanation than "the
fixture was unlucky". A discriminating fixture needs a parent with **no
unique index on the referenced columns** (goopg's ADD FOREIGN KEY does
not require one, unlike PG). Not run here; recorded for step (d).

**One field family diverges from PG, deliberately:**
`conpfeqop`/`conppeqop`/`conffeqop` stay NULL where PG populates them for
every FK. goopg's planner and enforcement read neither; a standby would
see the gap.

**Not touched, and not to be conflated with this round:**
`pg_constraint` returns 0 rows of *any* contype after a restart,
including the `'p'`/`'u'` rows the view synthesises from indexes that
demonstrably survive. That is a second, independent reload gap and
deserves its own round.

## 6. Review history

Three rounds, and the two blocks were substantive:

- **rev 1 BLOCKed on four findings.** The write surface was a third of
  the real one; the reload pointed at a database the writer never
  populates; the descriptor swap in §4; and a P0 that could not fail
  (an md5 over rows whose array columns are all NULL is byte-identical by
  construction).
- **rev 2 BLOCKed on three more.** I asserted a *"verified"* four-site
  write surface that was still missing three mutators — including
  `execAlterTableAlterConstraint`, the statement that sets `conenforced`.
  My routing contradicted my own mandatory-ALTER-form requirement. And I
  claimed a codec primitive was needed that already existed, with a
  `construct_md_array` trailing-pad fidelity fix a re-transcription would
  have dropped.
- **rev 3 APPROVE-WITH-NOTES**, four notes folded in: the `contype='f'`
  stamp guard (without which B3's table CHECK rows would be silently
  deleted on the next ALTER), VALIDATE's lock asymmetry, P5's mechanism,
  and two implementation checks.
- **The implementation was then BLOCKed on two more findings, both of
  which reproduced this round's own bug elsewhere.** Both fixed and
  mutation-verified:
  1. **ATTACH PARTITION's cloned FKs were not journalled** — the EIGHTH
     mutator. `cloneAndValidateAttachPartitionFKs` appends to the child's
     `ForeignKeys` (`operators_fk.go:615`) *after* the child's
     `syncTableToCatalogHeap` already ran, ~10 lines earlier in the same
     arm. Every attached partition silently lost referential enforcement
     at the next restart. Fixed with a post-clone
     `syncConstraintCatalogRow`; pinned and mutation-tested
     (`ForeignKeys=[]` without the fix).
  2. **Non-public schemas silently lost FK persistence** —
     `resolveFKCatalogKeys` hardcoded `Schema: "public"`, so an FK whose
     parent lived elsewhere failed to resolve, was never written, and
     vanished, **while the synthesised view kept displaying it** (the view
     resolves the unschemed `RefTable` by scanning the whole database).
     Fixed with `LookupTableByNameAnySchema`, which mirrors the view's own
     resolution and returns nil on cross-schema ambiguity rather than
     guessing a parent. Pinned and mutation-tested.

  Review also found three silent `continue`s where the scope had promised
  a warning; all four decline paths now `slog.Warn`, and an unresolvable
  `confdelsetcols` now drops the FK rather than silently widening
  `ON DELETE SET NULL (a)` to the whole key. `len(fks) > 0` was replaced
  by an unconditional assign, so the heap is the truth.

  Two further review notes, fixed rather than deferred, both in
  `resyncForeignKeyCatalogRow` (VALIDATE's narrow path):
  - **Stamp-skipped-but-write-happens.** It copied
    `syncConstraintCatalogRow`'s `if MaterializeWriterXID() == nil { stamp }`
    + unconditional write idiom. That is safe in the funnel, which re-syncs
    by conrelid idempotently, but here it would leave **two live rows with
    the same constraint OID** (one `convalidated=f`, one `=t`) and the
    reload would rebuild two entries for one constraint. Now returns the
    error instead.
  - **It never mirrored**, so after `VALIDATE CONSTRAINT` in `postgres`,
    `base/5/2606` kept `convalidated='f'` until some later table DDL
    happened to re-mirror. goopg's own reload mains at `DefaultDBOid` and
    was unaffected, but a standby reading base/5 would see the stale
    value. Now calls `mirrorConstraintCatalogFiles`.

Twice I cited a precedent while proposing the opposite of what it does —
`pg_attrdef` is in the mirror set *and* still mains at `DefaultDBOid`,
which is exactly why "mirrored" is not a licence to read `cat.DBOID()`.

## 7. Follow-ups this round created or confirmed

- `PhysicalTypeIsVarlena` has no `IsArray` arm — a latent bug for
  ordinary user `int4[]` columns, not just catalogs. Deserves a round.
- The PK/unique projection gap in §5.
- The six `deleteCatalogRowsForOID` call sites that hardcode
  `DefaultDBOid` are now FK-stamping sites with fixed routing; they look
  domain/composite-scoped, but a successor should confirm none can reach
  a per-DB table.
- Free win, named so nobody "fixes" it: `operators_tx.go:376` also calls
  `deleteCatalogRowsForOID`, so a rolled-back CREATE TABLE's FK row is
  now cleaned up automatically.
- R125 §7's other prose-only figure — 32.17 s for 40,000 × 8,000 FK
  validation — is **still un-remeasured**. It is the input to "step (c)
  is optional", so step (c)'s scope inherits that obligation. This round
  declares no FK and runs no validation, so it did not block here.
- Record in the B3 TODO that the FK slice of pg_constraint is already
  converted, and that the stamp predicate guards on `contype`.
- **An in-process per-DATABASE restart pin.** Blocked on the harness, not
  on effort (see §3's note). Worth a small harness addition, because this
  is the path step (d) rides.
- **`catalog.ForeignKey` should carry the parent's OID, not just an
  unschemed name.** Both sides of this round already compute it. PG
  resolves the referenced relation through the search path at DDL time
  and stores an OID, so ambiguity cannot survive there; goopg's
  name-keyed store is why `LookupTableByNameAnySchema` has to decline on
  cross-schema ambiguity, why `fkParentRel`'s raw byte compare is
  fragile, and why a renamed parent goes stale until a restart repairs
  it. One change retires all three.
- **The `MaterializeWriterXID` swallow was an idiom, not a local slip.**
  It is fixed in both `syncConstraintCatalogRow` and
  `resyncForeignKeyCatalogRow`; a successor touching another
  stamp-then-rewrite site should check for the same shape. It is
  invisible to tests because the error never fires in the harness.

## 8. Next

Step (c) is optional (NOT VALID FKs declare in O(1) since R125).
**Step (d)** — declare TPC-H's eight FKs and measure Q9 — is now
unblocked. Carried arithmetic, unchanged: with the FK firing goopg's
estimate is computable as `40,132 × 6,001,255 / 800,000 ≈ 301,050`, not
PG's 363,341, against ground truth 318,748. "Q9 → MATCH" remains
**speculative** — R125's reviewer reproduced a case where a corrected
cardinality left plan shape unchanged, and §5's fixtures are a fresh
reminder that a better estimate need not move anything.

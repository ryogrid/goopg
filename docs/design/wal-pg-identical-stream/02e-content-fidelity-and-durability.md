# 02e — Content fidelity + catalog durability (the remaining Phase-B open items)

| | |
|---|---|
| Status | draft — designed from 4-agent exploration (3 code/PG-fidelity Explore lenses + 1 architecture Plan agent); concrete constants spot-verified vs `./postgres` (relispopulated offset 129; operator OIDs 521/551/654; `DEFAULT_COLLATION_OID`=100; `_outConst` field order). Corrected the Plan agent's premise that a pg_operator generator was net-new — the 799-row seed already exists. |
| Date | 2026-07-18 |
| Branch | `wal-pg-stream-impl` |
| Oracle | PostgreSQL 18.3 — tree under [`postgres/`](../../../postgres/) |
| Scope | The three deferrals left after Phase B closed rmid-128: matview `IsPopulated` durability, `ALTER … RENAME` durability, and canonical `pg_node_tree` serialization (so a real PG18 standby can *evaluate/query* goopg's user defaults/stats/views). |
| Parent | [02d](02d-phase-b2-b5-overview.md) (RmgrGoopgCatalog retirement); [02b](02b-catalog-conversion-recipe.md) (the reload/heap-write idioms these reuse). |

## 0. Why this doc exists

Phase B made goopg journal every base `pg_catalog` table the way PostgreSQL does:
real heap-tuple writes + real rmgr records, no goopg-private `RmgrGoopgCatalog=128`
kind left in the **emitted** stream. A real PG18 standby therefore *replays*
goopg's catalog-DDL WAL without FATALing.

Three deferrals remain (all in [`.ralph/deferral_ledger.md`](../../../.ralph/deferral_ledger.md)),
in ascending scope:

1. **Matview `IsPopulated` across restart** — a goopg-facing durability bug.
2. **`ALTER VIEW/TABLE … RENAME TO` across restart** — a pre-existing goopg-facing
   durability bug surfaced while landing B5 Slice C.
3. **Canonical `pg_node_tree` serialization** — the crux. Three catalog columns
   (`pg_attrdef.adbin`, `pg_statistic_ext.stxexprs`, `pg_rewrite.ev_action`) store
   goopg **SQL text** rather than PostgreSQL's OID-resolved node-tree, and
   `pg_class.relhasrules` is forced FALSE for user views. The heap INSERT replays,
   but a standby cannot **evaluate** a default or **query** a view because the
   stored bytes are not a parseable PG node tree.

Items 1–2 are ordinary correctness fixes (data lost across restart) and land
first. Item 3's only payoff is standby-side *querying/evaluation* — goopg's own
restart and goopg↔goopg replication already work via the SQL-text convention —
but it is the north-star completion of "a PG standby sees goopg exactly as it
sees another PG," and the project owner has opted into the full build.

**Guiding rule (unchanged from the bundle):** emit the PG-shaped content at the
source, never convert. For item 3 that means resolving goopg's AST to an
OID-bearing node IR and serializing it in PostgreSQL's `outfuncs.c` format at
write time — not post-processing SQL text.

---

## 1. Item A — matview `IsPopulated` via `pg_class.relispopulated`

### Problem

Real PostgreSQL stores a materialized view's populated state in
`pg_class.relispopulated`. goopg's `buildUserPGClassRow`
(`internal/executor/pg18_user_catalog_rows.go:516`) hardcodes it `true`, and B5
Slice C removed the goopg-private `RecordKindCreateMatView` record that used to
carry `IsPopulated`. The reload (`loadViewsFromHeap`) now unconditionally sets
reloaded matviews to populated, so a `CREATE MATERIALIZED VIEW … WITH NO DATA`
matview reloads as *populated* after a restart — querying it returns an empty
result instead of the "has not been populated" error.

`tbl.IsPopulated` is set correctly at DDL time
(`operators_ddl.go:14327 tbl.IsPopulated = !s.WithNoData`;
`REFRESH … :14558` sets it true), and the live in-memory pg_class projection
already honours it (`catalog.go:6814`). Only the restart round-trip drops it.

### Design — make `relispopulated` faithful and read it back

Real PG stores `relispopulated` at byte offset **129** of the fixed pg_class
tuple part (confirmed against `postgres/src/include/catalog/pg_class.h` and
goopg's own `initdb.go` pgClassColDefs offset comments and
`relcache_init.go:1987` `buf[129]=1`). goopg already *writes* this byte; it just
never *reads* it back.

| Step | File | Change |
|---|---|---|
| Write | `pg18_user_catalog_rows.go:516` | `NewBoolDatum(true)` → `NewBoolDatum(!tbl.IsMatView \|\| tbl.IsPopulated)` (true for everything except an unpopulated matview). The index (`:579`) and composite (`:1711`) builders keep `true`. |
| Decode | `catalog/codec.go` | Add `pgClassOffRelIsPopulated = 129`; add `RelIsPopulated bool` to `PGClassRow` (:581); read it in `DecodePGClassPhysicalRow` (:844) via the existing `decodePGBool`. Byte 129 is inside the already-length-checked 144-byte fixed part → no bounds risk; `pgClassFixedPartSize` unchanged. |
| Reload | `initdb/open.go` after `:2911` | `if recovered.physical && tbl.IsMatView { tbl.IsPopulated = tr.RelIsPopulated }`. The `physical` guard prevents the legacy `DecodePGClassRow` path (which has no byte 129 → Go-zero `false`) from demoting a matview. |
| **Un-clobber** | `initdb/catalog_heap_reload.go` `loadViewsFromHeapForDB:539` | **Delete** the unconditional `if tbl.IsMatView { tbl.IsPopulated = true }`. It runs *after* `loadUserTablesFromHeap` and would overwrite the reloaded value. `tbl.IsPopulated` already carries the correct value. |
| Docs | `catalog_heap_reload.go:465-469`, `open.go:2889`, `operators_ddl.go:13268`, `wal/recovery.go:430` | Retire the "IsPopulated is a documented deferral" comments. |

**Sibling-path (encode↔decode):** the encoder already writes byte 129; this only
adds the read side. No other decoder consumes `relispopulated`.

### Verification

New restart-durability test: `CREATE MATERIALIZED VIEW mv_pop AS …` (populated)
and `CREATE MATERIALIZED VIEW mv_nodata AS … WITH NO DATA` → restart → assert
`relispopulated = 't'` for `mv_pop` and `'f'` for `mv_nodata`. The existing
`syntax_ddl_test.go:155` is in-process only and misses the reload path. The
layout-pinning tests (`TestUserPGClassRowFixedFieldsOID`) read by name/offset, so
the added decoder field is transparent — confirm they still pass. Risk: **low**
(read-only-additive; a re-init is needed only because the write value for
unpopulated matviews changes).

---

## 2. Item B — `ALTER VIEW/TABLE … RENAME TO` pg_class re-sync

### Problem (pre-existing)

`AlterTableRenameTable` (`internal/executor/operators_ddl.go:8037-8084`) calls
`o.ctx.Catalog.RenameTable(...)` (in-memory, mutates `tbl.Name`) and a
sequence-only persistence block, but for the general table/view case **never
re-syncs the pg_class heap row's `relname`**. So a renamed relation reverts to
its old name after a restart — verified: after
`ALTER VIEW v_old RENAME TO v_renamed` + restart, pg_class has `v_old` (count 1),
not `v_renamed` (count 0). This affects tables, plain views, matviews, and
(bare) sequences. It is unrelated to Slice C; the retired WAL records never
carried the name either (it always came from pg_class).

### Design — reuse the RENAME COLUMN delete+insert arm

`ALTER TABLE RENAME COLUMN` already persists correctly with an established idiom
(`operators_ddl.go:8162-8172`). Apply the same arm to `AlterTableRenameTable`,
after the sequence block (after `:8084`), still inside the case:

```go
if catalogHeapSyncAvailable(o.ctx) {
    if err := o.ctx.MaterializeWriterXID(); err == nil {
        xmax := o.ctx.Tx.XID
        for _, dbOid := range tableCatalogDBOids(o.ctx) {
            deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
        }
    }
    if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
        return fmt.Errorf("DDL catalog sync: %w", syncErr)
    }
}
```

Why this is correct and self-contained:

- **Keyed on `tbl.OID`** (stable across a rename). `deleteCatalogRowsForOID`
  (`:12737`) xmax-stamps the OID's pg_class / pg_attribute / pg_attrdef /
  pg_rewrite rows; `syncTableToCatalogHeap` (`:13172`) rewrites them from the
  current `tbl` — whose `.Name` is already the new name (`RenameTable` mutated it
  in place; the case captures `oldBare := tbl.Name` at `:8039` *before* the
  rename precisely because of this). So the fresh pg_class row and its
  relname-index entry carry the **new** name.
- **No new machinery.** pg_class has no per-OID TID cache (`classHeapTIDs` does
  not exist), so a targeted `updateHeapRowCanonicalPG` is not wired up; the
  pg_class-specific `resyncIndexClassHeapRow` (`:13369`) also uses delete+insert.
  This matches the established idiom.
- **base/1→base/5 mirror is automatic**: `syncTableToCatalogHeap` calls
  `mirrorTouchedCatalogsToPostgresDB` (gated on `heapDBOid == DefaultDBOid`), and
  `tableCatalogDBOids` covers the stamp side.
- **Views need nothing extra**: `ev_class` is the (unchanged) OID, so the
  pg_rewrite `_RETURN` rule is rewritten equivalently.

**Decision — run unconditionally (including sequences).** It additionally fixes
sequence pg_class-`relname` staleness and is orthogonal to the existing
`dropSequenceCatalogHeapRow`/`WALLogSequenceState` block
(`deleteCatalogRowsForOID` does not touch `pg_sequence`). Precondition to verify
before relying on this: `syncTableToCatalogHeap` must emit a correct
`relkind='S'` pg_class row for a sequence `tbl`; if it does not, guard the arm
with `!tbl.IsSequence` and file a follow-up for sequence rename durability.

### Verification

New durability test: `CREATE` a table and a view → `ALTER … RENAME TO` →
restart → assert pg_class has the new names and the old names are gone; the view
is still queryable. Update the "RENAME is a known deferral" note in
`view_pg_rewrite_durability_test.go:11-17`. Gate: `internal/executor` touch →
`scripts/tpch-spotcheck.sh` (or `TestPort_RegressSuite` where the TPC-H data dir
is absent) + the ALTER-heavy testport suites.

---

## 3. Item C — canonical `pg_node_tree` serialization

### 3.1 The problem — goopg has no resolved node tree to serialize

PostgreSQL stores `adbin` / `ev_action` / `stxexprs` as **post-analysis,
OID-resolved** S-expressions, e.g.:

```
adbin (DEFAULT 42):  {CONST :consttype 23 :consttypmod -1 :constcollid 0
                      :constlen 4 :constbyval true :constisnull false
                      :location -1 :constvalue 4 [ 42 0 0 0 ]}

ev_action (view):    {QUERY :commandType 1 … :rtable ({RANGETBLENTRY :relid 16xxx
                      :rtekind 0 …}) :jointree {FROMEXPR :fromlist ({RANGETBLREF
                      :rtindex 1}) :quals {OPEXPR :opno 521 …}} :targetList
                      ({TARGETENTRY {VAR :varno 1 :varattno 1 :vartype 23 …}
                      :resno 1 :resname client …})}
```

goopg has **none of the OID-resolved material** these need:

- Its parser AST is name-based: `ColumnRef` holds table/column *strings*
  (`parser/expr.go:377`), `FuncCall` holds a name string (`:478`, no `funcid`),
  `BinaryOp` is an `Op` enum not an operator OID (`:432`), constants carry no
  `consttype`.
- The analyzer (`internal/analyzer/analyzer.go`) only *type-checks*: `Analyze`
  returns `error` (`:187`), `analyzeExpr` returns `(catalog.Type, error)`
  (`:1090`) — it never rebuilds a resolved tree.
- The planner IR is goopg-private (`planner/plan.go:414` `ColumnRef{Index int,
  Type catalog.Type /*name*/, SourceTableIdx int16}` — a slot index, not
  `varno`/`varattno`; no type OID).
- The runtime resolves functions/operators **by name at eval time**
  (`executor/expr.go:1075`, dispatch on `x.Name`).

So there is no `consttype` / `funcid` / `opno` / `varno` / `vartype` anywhere to
serialize. The work is **four net-new pieces**, not just an `outfuncs` printer:
a resolver, an `outfuncs` printer, a `readfuncs` reader (for goopg's own reload
round-trip), and exact binary datum encoding for `Const`.

### 3.2 New package `internal/pgnodes`

A leaf package depending only on `internal/catalog` and `internal/parser`
(types); `executor`/`initdb` call *into* it, so there is no import cycle.

| File | Responsibility |
|---|---|
| `ir.go` | Resolved-IR structs — the PG primnodes/parsenodes SUBSET. Scalar: `Const`, `FuncExpr`, `OpExpr`, `RelabelType`, `CoerceViaIO`, `SQLValueFunction`. Query: `Query`, `RangeTblEntry`, `RTEPermissionInfo`, `FromExpr`, `RangeTblRef`, `TargetEntry`, `Var`. |
| `datum.go` | `Const` value ↔ raw PG in-memory datum bytes, per allowed type (§3.5). |
| `outfuncs.go` | IR → `pg_node_tree` S-expression text. Field order mirrors `postgres/src/backend/nodes/outfuncs.funcs.c` `_out<Tag>` **exactly**, one Go func per tag with an inline `// mirror: _out<Tag>` provenance comment. |
| `readfuncs.go` | `pg_node_tree` text → IR — a `pg_strtok`/`nodeRead` mirror (`read.c`) for goopg's own reload round-trip. |
| `resolver_expr.go` / `resolver_query.go` | goopg `parser.Expr` / `*parser.SelectStmt` + catalog → IR. |
| `rebuild.go` | IR → goopg parser AST (`Expr` / `SelectStmt`) for the reload path (§3.6). |
| `unsupported.go` | Shape-detection driving graceful degradation (§3.7). |

**Why these existing seams make the wiring surgical:**

- The `pg_node_tree` codec already passes a `KindBytes` datum through verbatim
  (`executor/codec.go:616` — this is how the PGLZ bootstrap blobs are stored).
  So each writer change is only `NewStringDatum(sqltext)` →
  `NewBytesDatum(canonicalVarlena)`; **no codec change**.
- The reload framework is already generic and correctly structured:
  `loadColumnDefaultsFromHeap` (`catalog_heap_reload.go:216`),
  `loadStatisticsExtFromHeap` (`:315`), `loadViewsFromHeap` (`:470`) are
  standalone unconditional per-`NamespaceDBOid` passes (they already run outside
  the M0114 pg_class cache-hit fast path). The only change inside each
  `*FromHeapForDB` is swapping the `parser.ParseExpr`/`parser.Parse` call for
  `pgnodes.Read → IR → rebuild`. A one-byte discriminator (a canonical dump
  begins `{`) selects canonical vs. legacy-SQL-text fallback, so pre-existing
  data dirs still reload.

### 3.3 OID resolution — reuse existing generated data

The resolver needs `(name, arg-type-OIDs) → funcid`, `(spelling, leftOID,
rightOID) → {opno, oprcode, opresult}`, and name → type-OID. goopg already ships
the data:

- **Functions**: `pgProcNamesByOID` + `pgProcArgTypeNamesByOID`
  (`catalog/pg_proc_names_generated.go`, 6811 lines) — a forward index is
  invertible from these at init. (`catalog.LookupBuiltinProc` is a ~205-name
  curated subset — insufficient alone.)
- **Operators**: the full 799-row pg_operator.dat **already exists** as
  `pgOperatorAllEntries()` in `internal/initdb/pg_operator_seed_data.go`
  (`{OID, Name, LeftType, RightType, ResultType, Code /*=oprcode funcid*/, …}`),
  generated by the existing `cmd/gen-pg-operator-data`. (`catalog.builtinOperatorsByKey`
  at `catalog.go:19186` is a 5-entry curated subset — insufficient.)
- **Types**: `catalog.TypeNameToOID` / the analyzer's `catalog.Type`.
- **User objects**: `cat.Routines()` (functions), user-operator/type registries,
  `LookupTableByOIDAllDBs` + a table's `.Columns[i].Ordinal+1` (attnum).

So the "generate operator data" slice (S0) is **not** a new generator — it is
building two forward indexes from existing data (exposing `pgOperatorAllEntries`
to `pgnodes` via `catalog`, or hosting the index in `catalog`), plus a small
`BinaryOp.Op` enum → operator-spelling table in the resolver
(`add`→`"+"`, `lt`→`"<"`, `concat`→`"||"`, …).

**OID-parity reasoning.** A standby is a real PG18 replaying goopg's WAL.
Built-in `funcid`/`opno`/`typeid` match because goopg uses PostgreSQL's bootstrap
OIDs. User-defined function/operator/type OIDs (≥16384) resolve on the standby
**because their `pg_proc`/`pg_operator`/`pg_type` rows were heap-journaled to it**
(Phase B1/B2 retired those catalogs to heap). Therefore verification MUST include
a view/default over a *user-defined function* to exercise the ≥16384 path
end-to-end.

### 3.4 Reload path — `pg_node_tree → IR → goopg parser AST` (structural rebuild)

Recommended over the two alternatives:

- **Not** `IR → SQL text → parser.Parse`: goopg has no expression deparser
  (`pg_get_expr` at `expr.go:9043` merely echoes stored text); regenerating
  exactly-parseable SQL (quoting, precedence) then re-parsing is strictly more
  fragile.
- **Not** `IR → planner IR` directly: would need a second planner entry point and
  duplicate the binder.

Everything downstream of reload consumes parser AST (`tbl.View` is
`*parser.SelectStmt`; `DefaultExpr` is `parser.Expr`), so `rebuild.go` inverts
the resolver for the supported subset: `RangeTblEntry.relid → relname` rebuilds
the `RangeVar`; `TargetEntry{Var} → ColumnRef` (attno → column name);
`OpExpr → BinaryOp` (opno → spelling → `Op`); `Const → IntegerConst/StringConst`
(datum bytes decoded to a literal). It shares the resolver's lookup tables. The
passes stay standalone/unconditional/per-`NamespaceDBOid` exactly as today.

### 3.5 Datum encoding (`datum.go`)

Wire form from `outDatum` (`outfuncs.c:347`) / `readDatum` (`readfuncs.c:600`):
`<declared-len> [ b0 b1 … ]` where each byte is a **signed-char decimal**
(`0xFF` → `-1`). By-value types always emit the 8-byte datum word; by-ref
(varlena) emit `VARSIZE_ANY` raw bytes. Reuse goopg's existing `encodeValuePG`
(`codec.go:173`) / `varlenaTextBytes` for the by-ref bytes; assemble the
by-value word directly.

| Type | typlen | byval | Emitted | Datum word / note |
|---|---|---|---|---|
| bool, "char" | 1 | ✓ | 8 | byte0 = value, rest 0 |
| int2 | 2 | ✓ | 8 | **sign-extend** to 64-bit LE |
| int4 | 4 | ✓ | 8 | **sign-extend** |
| oid | 4 | ✓ | 8 | **zero-extend** (unsigned) |
| int8 | 8 | ✓ | 8 | LE two's-complement |
| float4 | 4 | ✓ | 8 | IEEE-754 bits low 4 bytes, high 4 zero |
| float8 | 8 | ✓ | 8 | IEEE-754 bits LE |
| date | 4 | ✓ | 8 | int32 days since 2000-01-01, sign-extend |
| time/timestamp/timestamptz | 8 | ✓ | 8 | int64 microseconds LE |
| text/varchar/bpchar | -1 | ✗ | VARSIZE | **4-byte** varlena header (uncompressed) + data |
| bytea | -1 | ✗ | VARSIZE | 4-byte header + bytes |
| numeric | -1 | ✗ | VARSIZE | in-memory `NumericData` — reuse goopg's numeric heap encoder |

**Named traps** (each gets a targeted unit test): (1) by-value **sign-extension**
— a negative int4 must emit all-`0xFF` high bytes (printed `-1 -1 -1 -1`);
(2) signed-char printing (`int8(b)` decimals, `atoi`→`byte()` on read);
(3) text must use the **4-byte** varlena header, not goopg's short 1-byte, to
match PG's parse-time `Const` for the oracle byte-diff; (4) numeric — reuse the
existing encoder, do not hand-roll the base-10000 `NumericDigit` layout;
(5) `constcollid = 100` (DEFAULT_COLLATION_OID) for collatable types,
`consttypmod = n+4` for `varchar(n)`/`bpchar(n)`.

### 3.6 `relhasrules = true` — per-table and coupled

Add a `catalog.Table.RuleIsCanonical` flag, set `true` by `writeViewRewriteRow`
**only when canonical serialization succeeded**. `pg18_user_catalog_rows.go:511`
reads it instead of the hardcoded `NewBoolDatum(false)`. The `catalog.go` virtual
builders (`6954/7012/7058/7150/7204/7245`) are for **system/information_schema**
views (no user `_RETURN` rule) — leave them `"f"`; confirm each call site's
relkind/name first. The six nailed replication views
(`relcache_init.go:683`) already use `relhasrules=true` with hand-embedded
canonical blobs — untouched.

**The coupling is hard and normative:** `relhasrules=true` with a non-parseable
`ev_action` makes PG's relcache `RelationBuildRuleLock → stringToNode` FATAL at
relcache build. So the flag is set in the *same* write path that produced
canonical bytes, never independently. Any view that degrades to SQL text (§3.7)
keeps `relhasrules=false`.

Test lock-ins to update when this flips:
`internal/initdb/pg_stat_wal_receiver_nailed_test.go:111-114` (the
"relhasrules=false until ev_action compatible" assertion — the *nailed* views
stay a separate bootstrap path and keep their blobs; the assertion covers user
views now) and `internal/testport/e2e_failover_goopg_to_pg_test.go:278` (drop the
"view is not queried on the standby" caveat, add a positive standby `SELECT`).

### 3.7 Graceful degradation (mandatory, built into S2/S3)

`unsupported.go` runs an **all-or-nothing** subset check before serializing. On
reject:

- **Defaults / stats**: fall back to storing goopg SQL text as
  `pg_node_tree`-as-text (today's behavior). Replay stays safe; the standby just
  cannot evaluate that one default.
- **Views**: store SQL text **and keep `relhasrules=false`** for that one
  relation. PG replays the heap row, the view exists, PG never tries to expand a
  rule it can't parse.
- **Never FATAL, never partial-emit.** Either fully canonical
  (→ `relhasrules=true`, standby-queryable) or fully SQL-text
  (→ `relhasrules=false`, replay-only), per object.

**In-scope (first cut):** constant + single built-in-function/operator column
defaults; single-base-relation views/matviews with a flat target list of `Var`s
and simple scalar expressions and an optional `WHERE` of
`Const`/`Var`/`OpExpr`/`FuncExpr`. **Out of scope (detected → degraded):**
multi-table/join views, subqueries, aggregates/GROUP BY/HAVING/window, set ops,
LATERAL, exotic/composite types not in the datum table. A unit test asserts every
unsupported shape degrades rather than emitting a malformed tree; an E2E asserts a
join-view still replays green on the standby with `relhasrules=false`.

### 3.8 Slices (each gated: build/vet + unit + testport + E2E-failover green)

| Slice | Content | Gate | Size |
|---|---|---|---|
| **S0** | Forward proc + operator OID indexes from the existing generated data; `BinaryOp.Op`→spelling table. | Unit test pinning ~30 known rows (`int4>int4→521`, `text\|\|text→654`, `int4+int4→551`) vs `pg_operator.dat`. No WAL/e2e. | Small |
| **S1** | `ir.go` + `datum.go` + `outfuncs.go` + `readfuncs.go` for the scalar subset. | Golden round-trip: hand-built IR → `Out` → text byte-equal to a real-PG `nodeToString` golden (via `scripts/pg-oracle-diff.sh`, `:location`→`-1` normalized), then `Read → IR'` deep-equal. No writer wired → no e2e. | Medium-large |
| **S2** | `resolver_expr.go` + scalar `rebuild.go`; wire `writeAttrdefRow` + stxexprs writer (guarded by `unsupported.go`); swap the two scalar reload passes. | **Adversarial standby-eval E2E**: goopg primary `CREATE TABLE t(a int DEFAULT 40+2, b text DEFAULT upper('x'), c int DEFAULT -1)`; PG standby `INSERT … DEFAULT VALUES`, assert the row `=(42,'X',-1)` AND equals goopg's own insert (oracle-diff). | Medium |
| **S3** | `resolver_query.go` + Query/RTE/`TargetEntry`/`Var` tags in out/read/rebuild; wire `writeViewRewriteRow` + the `RuleIsCanonical`/`relhasrules` flip; swap `loadViewsFromHeap`. | **Standby-query E2E**: goopg primary `CREATE VIEW v AS SELECT client, src FROM bench_log WHERE client>0` + one view over a **user-defined function**; PG standby `SELECT * FROM v` returns the correct rows, byte-equal to goopg's own. Update test lock-ins. | Large (`_outQuery`/`_outRangeTblEntry` are 46 fields each — transcribed in exact order, pinned by a structural test) |
| **S4** | Coverage + hardening: more datum types, `CASE`/`BoolExpr`/`NullTest` in target lists, and the byte-diff oracle gate (emitted `ev_action`/`adbin` == real-PG18's for the identical DDL). | Incremental sub-slices. | Incremental |

The nailed replication views keep their embedded `.dat` blobs (separate
bootstrap path) throughout.

### 3.9 Verification is adversarial — silent mis-evaluation is the failure mode

One wrong OID or datum byte makes the standby return a plausible-but-wrong answer
with no error. Layered defenses:

1. **Golden byte-diff (S1, S4)** — `outfuncs` output asserted byte-equal (after
   normalizing `:location` to `-1`) to a real PG18 `nodeToString` of the same
   expression/query, captured via `scripts/pg-oracle-diff.sh`. Catches wrong
   field order / OID / datum bytes at the unit level.
2. **Adversarial standby-eval/query E2E (S2, S3)** — the gate does not merely
   assert "no FATAL on replay"; it makes the **standby compute** and asserts
   equality with goopg's own result. Include (a) a negative-constant default
   (sign-extension), (b) a text-constant default (header/collation), (c) a view
   over a user-defined function (≥16384 OID path).
3. **`readfuncs` round-trip (S1+)** — `AST → resolve → IR → Out → text → Read →
   IR' → rebuild → AST'`, asserting `AST'` re-plans to the same result. Pins
   goopg's own reload independent of PG.
4. **Structural pins** — a unit test asserts the emitted per-tag field count and
   order against the `_out<Tag>` field list transcribed from `outfuncs.funcs.c`,
   so a future PG-version bump that reorders fields is caught.

---

## 4. Sequencing and gates

- **Order**: items A and B first (small, safe, independent, unblock the two
  goopg-facing durability bugs), then item C S0 → S4.
- **Per landing**: `go build ./...` + `go vet`; the touched-package unit suites
  (`catalog`/`executor`/`initdb`/`wal`/new `pgnodes`); `-race` on touched
  storage/WAL packages; the new durability + round-trip tests; `TestPort_RegressSuite`
  (the superset gate where the TPC-H data dir for `scripts/tpch-spotcheck.sh` is
  absent) after executor/catalog touches; `TestE2E_FailoverGoopgToPG` extended
  with the item-C standby-eval assertions.
- **Data-dir re-init** after any on-disk change (item A's write value change;
  item C's `adbin`/`ev_action` byte-format change).
- **Commit discipline**: pathspec, never stage `postgres`; `--no-verify` after
  the manual gates (the pre-commit smoke can't bind its control socket in this
  deep worktree — sun_path > 108); Co-Authored-By trailer; push to
  `origin wal-pg-stream-impl`. Update the deferral ledger + `IMPLEMENTATION-TODO.md`
  + memory per landing.

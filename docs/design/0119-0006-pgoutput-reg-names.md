# M0119-0006 — pgoutput renders reg* column values as names, not numeric OIDs

**Status:** accepted (2026-08-14, 82nd slice). Resolves deferral-ledger row 1353.

## The bug

goopg's logical-replication `pgoutput` decoder rendered a `reg*` column value as a
numeric OID on the wire: `regclass` → `1259`, `regclass[]` → `{1259}`. PG 18.3's
text-mode pgoutput renders the *name*: `pg_class` / `{pg_class}`. The subscriber's
apply worker re-parses the literal against its own catalog, so a numeric OID still
round-trips to the right object *only* by luck of a shared OID space — it is not
what PG emits.

## The root cause (and a correction to the row's premise)

Row 1353 named the array arm and called the divergence "wire-text cosmetic only."
Both halves understate it:

1. **It is not cosmetic.** A `reg*` wire value is the object's *identity*; PG emits
   the name so the subscriber resolves it against the subscriber's catalog. Emitting
   a bare OID only matches when the OIDs happen to agree, and it is byte-divergent
   from PG either way.
2. **The scalar arm was wrong too.** `pgoDecodePhysicalValue`'s oid arm carried
   `regproc`/`regprocedure`/`regclass`/`regtype`/`regrole`/`regcollation` alongside
   `oid`/`cid`/`xid`, and its doc comment justified the numeric form by citing
   `regclasssend`/… — a conflation of PG's two serializers. `logicalrep_write_typ`
   (postgres `src/backend/replication/logical/proto.c:848`) uses
   `OidOutputFunctionCall(typclass->typoutput, …)` for **text** mode — a `reg*`
   type's `typoutput` is `regclassout`/`regprocout`/…, which convert the OID to a
   name (`regproc.c:940` "converts class OID to class_name"). The 4-byte-OID form
   (`regclasssend`) belongs to **binary** mode (`typsend`, proto.c:841). goopg's
   `pgoDecodePhysicalValue` is text-only and had silently shipped the binary image.

## Design decisions

1. **`executor.RegOut` stays the single source of truth.** A new exported
   `RegOutRenderer(cat, qualify, dbOid...)` returns a closure over `RegOut` (the
   existing port of `reg*out`, already faithful for name rendering, schema
   qualification, and the numeric fallback for an unresolvable/dangling OID). No
   name-resolution logic is reimplemented in the WAL layer.
2. **Thread it as a leaf value, not an import.** `internal/wal` cannot import
   `internal/executor` (the executor already imports `internal/wal` — a cycle).
   `CatalogSnapshot` gains a `RegOut func(typeName string, oid uint32) string`
   field (nil = numeric fallback), and `BuildCatalogSnapshot` takes it as a trailing
   variadic so existing callers compile unchanged. The publisher walsender
   (`internal/server/logicalwalsender.go`, which *can* import the executor) binds
   `executor.RegOutRenderer(im, false)` — server→executor→wal, one direction, no
   cycle.
3. **`oid`/`cid`/`xid` stay numeric.** They have no name form in text-mode pgoutput
   (their `typoutput` is `oidout`/`cidout`/`xid8out`, which are decimal), so they
   keep the numeric arm even when a renderer is present.
4. **Both twins change together** (Hard-won Rule #2): the scalar reg* arm is split
   out of the oid arm to consult the renderer, and the array arm switches
   `pgarray.RenderText` → `pgarray.RenderTextStyled(…, OutputStyle{RegOut: …})`,
   which `DecodeElemStyled` already consumes for reg* elements.
5. **`qualify=false`.** The walsender has no session `search_path`, so the renderer
   emits bare names. Correct for objects on the default path (`pg_catalog` +
   `public`); a regclass in a non-public schema renders bare where PG
   schema-qualifies — a new deferral (below).

## Sibling paths audited

- **scalar ↔ array**: both `pgoDecodePhysicalValue` arms fixed together; pinned by
  `TestPgoDecodeRegFamilyMatchesPGNativeLayout` (reworked) + `TestPgoDecodeArrayColumns`.
- **renderer ↔ numeric fallback**: nil renderer keeps the 4-byte numeric image; the
  reworked test pins both directions.
- **wal decode ↔ executor render**: the wal layer never reimplements name resolution;
  it calls `RegOut`, so the two cannot drift.

## Tests

- `internal/wal/pgoutput_reg_identifier_test.go` — `TestPgoDecodeRegFamilyMatchesPGNativeLayout`
  reworked: the send/out conflation comment fixed; nil-renderer cases pin the
  numeric fallback, renderer cases pin names via a synthetic closure (wal cannot
  import the executor), `oid`/`cid` stay numeric under a renderer, and
  InvalidOid/`0xFFFFFFFF` exercise the renderer's own `-`/numeric fallback.
- `internal/wal/pgoutput_array_test.go` — two `regclass[]` cases: renderer →
  `{pg_class,pg_class}`, nil → `{1259,1259}`.
- `internal/server/pgoutput_reg_names_test.go` — `TestPgoutputSnapshotRegOutRendererWired`
  builds a real `catalog.NewInMemory()` + `executor.RegOutRenderer` and asserts
  `snap.RegOut("regclass", 1259) == "pg_class"` + dangling-OID numeric fallback.
- Mutation-checked: deleting the scalar arm, the array threading, or the
  snapshot binding fails its test.

## Gates

`go test ./internal/wal/ ./internal/server/`; pre-commit units; `scripts/tpch-spotcheck.sh`
(Q12=2, Q13=35). All PASS.

## Deferred (ledger row appended)

- **Off-path schema qualification**: `qualify=false` renders a bare name for a
  regclass in a non-public schema where PG's walsender schema-qualifies via its
  `search_path`. The walsender has no search_path to compute visibility from.
- **Cross-DB regclass resolution**: the renderer binds no dbOid, so a regclass OID
  resolves against `DefaultDBOid`; a logical slot is per-database and the walsender
  carries no slot dbOid to scope the lookup.

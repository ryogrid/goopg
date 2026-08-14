(81st slice landed and committed — M0119-0006 continues)

**This loop (2026-08-14):** resolved deferral row 1352 (btree array-key 0A000). A
B-tree index over a `reg*[]` array column — AND over a scalar `reg*` column, which
the row's premise wrongly assumed already worked — now builds and maintains.
`encodeBTreeKeyForColumn` gained a reg* arm (name→OID via `regIdentifierInput` →
`btree.EncodeInt8(int64(oid))`), producing the **8-byte unsigned oidcmp** key (NOT
4 bytes — the stored element is 4-byte OID but the key is 8-byte oidcmp, every reg*
type defaulting to oid_ops). `regproc` moved out of the numeric-only `isOidType`
arm into the new `isRegType` (six members). ctx+pos threaded through the
build/probe/maintain/arbiter callers (nil-ctx numeric passthrough for the
fingerprint path). Decode twin landed together: `arrayKeyElemRenderer` reg* arm
(OID→name via `st.RegOut`), making reg*[] decodable automatically → IOS activates.
Commit `05897026`. Design `0119-0006-btree-reg-array-key-oidcmp.md` (+README bf).
Gates PASS: targeted btree-key probe, pre-commit units, tpch-spotcheck (Q12=2/Q13=35).

**Verified NOT deferrals (do NOT re-file):** the implementer's two "PG-semantics
discoveries" were both false positives. (1) "scalar reg* SELECT renders numeric" is
a unit-test VM-fixture artifact — production SELECT renders names via
`internal/server/dispatch.go appendTypedCellText` → `RegOut` (copy_text.go:294-295
documents it); the fixture's runQuery renders a KindInt OID datum generically.
(2) "scalar regclass IOS without VM bit" is a pre-existing planner nuance (runtime
falls back to heap), not a regression.

**Next step:** continue M0119-0006 per the banner. Pick ONE open deferral row:
- Natural successor: WAL pgoutput reg*[] render (row 1353) — `pgoDecodePhysicalValue`
  renders `{1259}` where local SELECT renders `{pg_class}`; thread a catalog/RegOut
  into the pgoutput decode (wal-layer change, own pgoutput round-trip gates).
- Older open reg* rows: 1307 (22P04 unwrap for non-reg* COPY), 1340 (role
  case-preservation), 1343 (user-type namespace field), 1344 (routineArgTypeName
  case-fold), 1347 (empty-schema visibility proxy), 1351 (OID-per-arg capture).
- Or pivot to the original M0119-0006 scope: `box`/`int4range` key encodings and the
  whole-database (unscoped) pg_amcheck run (ledger rows 2026-08-10).

**NIGHTLY:** `ci/logs/action-items.md` run 20260814-011711 (enum, oid) already filed
AND fixed this morning — nothing new to file (confirmed in fix_plan.md lines 994/1011).

(80th slice landed and committed — M0119-0006 continues)

**This loop (2026-08-14):** resolved deferral row 1306. Array-of-`reg*` columns
(`regclass[]`/`regtype[]`/`regprocedure[]`/`regrole[]`/`regcollation[]`/`regproc[]`)
now store **4-byte OID elements** (resolved name→OID on input, rendered OID→name on
output) instead of the prior silent varlena-text elements — the descriptor-vs-blob
disagreement (`elemtype=25` vs `_regclass`) the scalar reg* family (66th–68th) fixed
for non-array columns. Sibling triplet: `pgarray.ElemTypeInfo` arms, `encodeArrayElem`
name→OID via `regIdentifierInput` (ctx+pos threaded through `EncodeRowPGCtx`/
`encodeValuePGCtx`/`encodeArrayValuePGCtx`), `DecodeElemStyled` OID→name via the
executor-threaded `OutputStyle.RegOut` value (pgarray stays leaf). Commit `23c43a88`.
Design `docs/design/0119-0006-reg-array-element-fidelity.md` (+README `0119-0006be`).
Gates PASS: pgarray+executor units, pre-commit units, tpch-spotcheck (Q12=2/Q13=35),
`TestPort_RegressSuite` (233 s). Mutation-checked.

**Concurrency (do NOT re-block):** the SessionStart guard is a FALSE POSITIVE — one
master `ralph_loop.sh` (`400006`) plus its per-iteration child (`550360`, the wrapper
for this session). No second writer; no `.git/index.lock`.

**Two new deferral rows filed this loop (at ledger tail):**
- btree array-key 0A000 — an indexed `regclass[]` column still errors on ANY insert
  (`encodeArrayBTreeKey` → scalar element encoder has no reg* arm). Pre-existing,
  unchanged by this slice, but now heap=4-byte vs index=error.
- WAL `pgoutput` reg*[] renders numeric (`{1259}`) vs local SELECT names
  (`{pg_class}`) — NEW sibling divergence (nil `OutputStyle.RegOut` in
  `pgoDecodePhysicalValue`). Wire-text cosmetic; needs catalog threading into wal.

**Next step:** continue M0119-0006 (pg_amcheck server tier) per the banner. Pick ONE
of the open deferral rows and brief a researcher → implementer as this loop did:
- Natural successors to this slice: the btree array-key 0A000 row (indexed reg*[]
  columns error outright) or the WAL pgoutput reg*[] render row (both newly filed).
- Older open reg* rows: 1307 (22P04 unwrap for non-reg* COPY errors — broad),
  1340 (role case-preservation — blocked on catalog change), 1343 (user-type
  namespace field), 1344 (routineArgTypeName case-fold), 1347 (empty-schema
  visibility proxy), 1351 (OID-per-arg capture).
- Or pivot to the original M0119-0006 scope: `box`/`int4range` key encodings and the
  whole-database (unscoped) pg_amcheck run (ledger rows 2026-08-10).

**NIGHTLY:** `ci/logs/action-items.md` run `20260814-011711` (enum, oid) is already
filed AND both fixed this morning — nothing new to file. Two stray untracked test
files predate this loop (`internal/pgnodes/int2_cast_test.go` M0123-S4,
`internal/testport/datconnlimit_durability_test.go` M0122-0006) — leave alone.

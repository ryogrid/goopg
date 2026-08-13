(idle — nothing in flight)

Completed this loop: **M0119-0006 66th slice** — the reg* family and `cid`
store as 4-byte OIDs, and the regtypein 42704 miss-path fires (ledger row
1300's reg*/cid half resolved). The untracked `reg_identifier.go` WIP the 65th
slice's gate note named as its only failure is now LANDED: `regclass`/`regtype`/
`regprocedure`/`cid` moved from varlena TEXT to 4-byte LE OIDs (typalign 'i')
across all three physical decoders — heap codec (`codec.go`), binary COPY
(`copy_binary.go`), and the `pgoDecodePhysicalValue` third twin
(`internal/wal/pgoutput.go`, which was reading the 4-byte image through the
varlena fall-through = silent garbage) — plus `regIdentifierInput` (new
`internal/executor/reg_identifier.go`) as the name→OID input half routed from
`coerceRowForConstraintChecks` (regclassin/regtypein/regprocin semantics,
42P01/42704/42883 on a miss). The 65th slice's blocker was the
`TypeNameToOID` OIDText-fallback defeating the `oid != 0` test; the miss test
is now the established `oid != OIDText || name=="text"` idiom and 42704 fires.
`regrole`/`regcollation` carry forward in a NEW ledger row (no name→OID seam at
coerce time). Gates: pre-commit units PASS, regress suite PASS (346 s),
tpch-spotcheck PASS (Q12=2, Q13=35). Commit `4d8692d4` on `try-cost-optimization`.
Design `docs/design/0119-0006-reg-identifier-family-storage.md` + README row +
ledger row 1300 resolved + fix_plan 66th-slice entry.

**Carry-forward for a later loop:** `regrole`/`regcollation` still store as
varlena TEXT in the heap (upstream: 4-byte identifiers); fixing them needs a
role/collation name→OID lookup at coerce time — per-instance initdb-minted OIDs
mean a static table like `TypeNameToOID` will not do. See the new ledger row
under 2026-08-14 (66th slice).

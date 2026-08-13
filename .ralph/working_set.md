(idle — nothing in flight)

Completed this loop: **M0119-0006 67th slice** — `regrole` (4096) and
`regcollation` (4191) join the object-identifier family as 4-byte LE OIDs
(ledger row 1302 resolved). The name→OID seam the 66th slice said was missing
now exists: `regIdentifierInput` gains `regrole` via `InMemory.RoleOID`
(qualified name → 42602 invalid name syntax, miss → 42704) and `regcollation`
via the new exported `InMemory.CollationOIDByName` (builtin-then-user, REUSING
`builtinCollationOIDByName` + `UserCollationOIDByName` — no third map copy;
qualified names via `FindCollation`, miss → 42704 `collation "%s" for encoding
"UTF8" does not exist`), routed from `coerceRowForConstraintChecks`. All three
physical-codec twins moved to the 4-byte layout (heap `codec.go`, binary COPY
`copy_binary.go`, `pgoDecodePhysicalValue` in `internal/wal/pgoutput.go`), the
family-wide `parseDashOrOid` latent gap closed (`'-'` → 0, pure-digit →
numeric OID, uint32 overflow → 22003), and `appendTypedCellText` renders
regrole/regcollation as names (regroleout/regcollationout, OID 0 → "-",
dangling → numeric) so SELECT output stays `postgres`/`C`. Gates: pre-commit
units PASS, regress suite PASS (245 s), tpch-spotcheck PASS (Q12=2, Q13=35).
Design `docs/design/0119-0006-regrole-regcollation-4byte-storage.md` + README
row + ledger row 1302 resolved + new row filed + fix_plan 67th-slice entry.

**Carry-forward for a later loop:** TEXT/CSV COPY of a `reg*` column still
renders its numeric OID in both directions — the family-wide gap the 66th slice
already shipped for `regclass`/`regtype`/`regprocedure`/`cid` (lossless
cross-engine, but not PG-faithful text). Fixing it needs a catalog handle
threaded through `RunCopyTo`/`EncodeCopyTextRow`/`EncodeCopyCsvRow`/
`datumToCopyText` and the COPY FROM row routed through
`coerceRowForConstraintChecks`. See the new ledger row under 2026-08-14 (67th
slice).

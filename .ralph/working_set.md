(82nd slice landed and committed — M0119-0006 continues)

**This loop (2026-08-14):** resolved deferral row 1353 (WAL pgoutput reg*[] rendered
numeric OIDs). The logical-replication `pgoutput` decoder now renders a `reg*` column
value as its NAME (`pg_class` / `{pg_class}`), matching PG 18.3's TEXT-mode pgoutput.
Oracle-verified root cause: `logicalrep_write_typ` (proto.c:848) serializes text mode
via `OidOutputFunctionCall(typclass->typoutput, …)` = `regclassout` → name
(regproc.c:940); the 4-byte-OID form is BINARY mode's `typsend`. goopg's text-only
`pgoDecodePhysicalValue` had shipped the binary image — and its SCALAR arm too (the six
reg* types rode the oid/cid/xid arm behind a `regclasssend` comment), so both twins
were fixed, not just the array arm the row named. Fix threads `executor.RegOut` (the
existing reg*out port, single source of truth) into the wal layer as a leaf closure:
`CatalogSnapshot.RegOut func(typeName, oid) string` (nil = numeric fallback), bound by
the publisher walsender via new exported `executor.RegOutRenderer(im, false)`
(server→executor→wal; wal→executor is a CYCLE so the renderer is a value, not an
import). `oid`/`cid`/`xid` stay numeric. Commit `07102c95`. Design
`0119-0006-pgoutput-reg-names.md` (+README row `0119-0006bg`). Gates PASS: wal+server
packages, pre-commit units, tpch-spotcheck (Q12=2/Q13=35), pgbench smoke.

**Correction to the row (important):** the row's "wire-text cosmetic only" framing was
WRONG — a reg* wire value is the object's identity (resolved on the subscriber), so
numeric-vs-name is a real divergence, not cosmetic. And its resume note "wal already
imports the executor (no cycle)" was also wrong — wal→executor IS a cycle; the renderer
is threaded as a value from the SERVER, which imports both.

**New deferral filed (open):** the pgoutput renderer binds `qualify=false` + no `dbOid`,
so (1) a regclass in a non-public schema renders a BARE name where PG schema-qualifies,
and (2) a regclass resolves against DefaultDBOid (no slot dbOid). Resume:
`internal/server/logicalwalsender.go:69` — thread the slot's search_path + dbOid into
the `RegOutRenderer` binding.

**Next step:** the narrow reg* RENDER family is now essentially closed. Remaining open
reg* rows (1307, 1340, 1343, 1344, 1347, 1351, + the new off-path/dbOid row) are all
broad catalog-representation / session-state changes, not narrow rendering fixes. The
natural pivot is the ORIGINAL M0119-0006 remaining scope named in the banner:
`box`/`int4range` btree key encodings, then the whole-database (unscoped) pg_amcheck
run (ledger rows 2026-08-10). Pick ONE.

**Gates run:** `go test ./internal/wal/ ./internal/server/` PASS; pre-commit units PASS;
`scripts/tpch-spotcheck.sh` Q12=2/Q13=35 PASS; pre-commit pgbench smoke PASS (0 failed).

**NIGHTLY:** nothing new to file (action-items.md run 20260814-011711 already filed and
fixed this morning; confirmed in fix_plan.md lines 994/1011).

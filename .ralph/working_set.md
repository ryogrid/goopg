(idle — nothing in flight)

Last loop: M0119-0006 13th slice — B-tree key encodings for `int2`/`oid`/`bool`/
`bytea`/`time`. COMPLETE and committed, pushed.

The defect: `isSupportedBTreeKeyType` rejected all five, so `CREATE INDEX` (even
a `PRIMARY KEY (smallint_col)`) raised `0A000 btree v0 only supports int4 /
numeric keys`. Not a corner — pg_amcheck's own upstream fixtures index `oid`.

Fix: new `internal/executor/btree_scalar_keys.go` — predicates + one encoder
(`encodeScalarBTreeKey`, routed from `encodeBTreeKeyForColumn` before its type
switch), one unknown-literal coercer (`coerceScalarKeyStringDatum`, reusing
`byteaIn`/`parseTimeString`/`evalCast`), one decoder (`decodeScalarBTreeKey`)
that BOTH key-decode siblings (`decodeIndexKeyColumn`, `decodeBTreeKeyToDatum`)
route to before their own switches — their shared `default:` arm reads any 8
leading bytes as an enum float8 and never errors, so an 8-byte oid/time key would
otherwise decode as a bogus enum. Orders reproduce the default opclass, NOT the
text: **oid widens to the INT8 key because `oidcmp` is UNSIGNED** (int4 would
sort OIDs ≥ 2³¹ below OID 0); bytea rides `EncodeVarchar` (escaped,
0x00-terminated = `byteacmp` memcmp-then-shorter-first, NUL-safe, composite-safe);
time = int64 micros-of-day. `timetz` DECLINED (two-part comparison).

Method note: the PG reference cluster was already running — connect by ABSOLUTE
socket path: `postgres/local_install/bin/psql -h
/home/ryo/work/goopg/goopg/bench/tpch/runtime/sockets -p 65432 -U postgres -d
tpch` (a relative `-h` is treated as a hostname and fails to resolve).

Design: `docs/design/0119-0006-scalar-index-key-encodings.md` (+ README row
`0119-0006h`). 2 ledger rows (timetz; and the INSERT-path gap this surfaced —
`VALUES ('true')` into a bool column raises XX000, codec bool arm demands
KindBool).

Gates run: 6 new tests PASS, non-vacuous (routing disabled ⇒ all fail);
`go test ./internal/executor/ ./internal/access/btree/ ./internal/planner/` PASS;
units precommit PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35);
pgbench hook PASS at commit; `make ralph-state-guard` OK (auto-repaired the stale
completed marker).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M0130 all `[x]`; M-NIGHTLY run 20260809-020705 fully triaged (all 49 filed/closed).
Remaining M0119-0006: checkunique posting-list duplicates, `box`/`int4range`/
`interval`/`timetz` encodings, the array DECODE arm, unscoped whole-DB pg_amcheck.

In-flight: none.

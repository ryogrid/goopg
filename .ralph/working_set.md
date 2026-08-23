(idle — nothing in flight)

Task just completed: M0134-0087 (xid.sql). Landed a three-site sibling-path
fix: (1) evalCast (internal/executor/expr.go) had NO case for "xid"/"xid8" —
CastExpr syntax fell to the default arm and returned strings unchanged
(no parsing, no validation); added a shared case reusing parseXid/parseXid8.
(2) appendTypedCellText (internal/postmaster/dispatch.go), shared by the
simple- and extended-query TEXT paths, had no "xid8" case, so a wrapped
uint64 rendered SIGNED ("-1" instead of 18446744073709551615) over the wire;
added strconv.AppendUint. (3) pgUnsignedIDFromDatum's KindString branch
(internal/executor/codec.go) routed xid/xid8 through decimal-only
coerceStringToInt64, so INSERTing a hex/octal xid8 literal raised a spurious
22003; special-cased xid/xid8 to parseXid/parseXid8 (also added octal support
to parseXid8, matching parseXid). Committed as 3787a264 and pushed. Tests:
internal/executor/xid_cast_test.go, 10 new cases in
pg_unsigned_id_wrap_test.go, internal/postmaster/xid8_output_test.go — all
pass, plus full precommit gate (units + pgbench smoke) PASS. Ledger row
appended 2026-08-24 M0134-0087, fix_plan.md M0134-0087 marked PARKED.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0088 (alter_generic.sql,
status `not-tried`)**. Size it live against ./postgres oracle via
scripts/pg-regress-runner.sh --verbose alter_generic (background, generous
timeout — setup alone takes ~2-3 min). CAUTION carried forward from
M0134-0086: watch `ps -o rss` on the goopg PID while any regress file runs
(some cases drove RSS to 20+ GB in <2 min); kill -KILL promptly (never bare
pkill -f) if RSS climbs unbounded before deciding whether it's worth fixing
first. Deferred buckets left in M0134-0087 for a future resume: xid8 min/max
compares stored int64 SIGNED not UNSIGNED (Datum has no type tag
distinguishing xid8 from int8 — same gap TimeSubtype solved for KindTime,
never extended here); xid wrongly ALLOWS relational operators (PG raises
42883, no btree opclass); pg_input_error_info blank message/sql_error_code
for xid/xid8; 3 more undispatched pg_snapshot/xid8 builtins
(pg_visible_in_snapshot, pg_current_xact_id_if_assigned, pg_xact_status);
oid/regproc's own decimal-only string coercion (real PG is base-0 too, left
out of scope — own regress file + wider blast radius across regclass/
regtype/regrole/regcollation/cid).

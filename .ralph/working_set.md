(idle — nothing in flight)

Last landed: DU-002 slice 172 (loop #139) — multi-parent legacy inheritance
(`INHERITS (a, b)`) round-trips through pg_dump. CLEAN POSITIVE (verified, no fix
needed): pinned as a regression guard on the slice-170 machinery.

Why it already works: the INHERITS column-merge dedup keeps a column present in
both parents once (`shared`; M0097-0046, with the "merging multiple inherited
definitions" notice); the slice-170 marker loop iterates the FULL merged column
set so EVERY inherited column gets Inherited=true (attislocal=false), so pg_dump
omits them; pg_inherits VirtualRows emits one row per parent with inhseqno=i+1
from the ordered InheritsParentOIDs, so pg_dump re-emits the parents in the SAME
declaration order.

Fixture: minh_a(shared,a_only) + minh_b(shared,b_only) →
minh_child(own_col) INHERITS (minh_a, minh_b). 3 assertions: (1) ordered
`INHERITS (public.minh_a, public.minh_b)` clause, (2) local `own_col boolean`
survives in the child block, (3) `shared`/`a_only`/`b_only` NOT re-emitted there.

Files: internal/testport/pgdump_connsetup_test.go (fixture + assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 172 section), .ralph/fix_plan.md
(loop #139 note under M0110-0001).
Gates: gofmt clean; go vet ./internal/testport/ clean;
TestPort_PgDumpConnectionSetup PASS (2.56s, NOT skipped); pgbench pre-commit smoke
on commit (.githooks/pre-commit).

Next (slice 173 candidates): (1) dedicated MINVALUE/MAXVALUE keyword-AST-node —
parser collapses keyword `MINVALUE` vs text-RANGE literal `'MINVALUE'` into the
SAME StringConst{Value:"MINVALUE"}, so a literal text bound misrenders as the
unbounded sentinel (HIGHER RISK: touches partition routing compareBoundToKey +
catalog string bound representation; rare edge case). (2) column-level
STORAGE/COMPRESSION dump fidelity (needs parser keywords). See deferral ledger.

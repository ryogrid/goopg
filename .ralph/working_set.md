(idle — nothing in flight)

Last landed: DU-002 slice 122 (loop #86) — a multi-serial table (two serial
columns on one table) dumps byte-identically. The multi-column counterpart to
slice 121's single serial. NO production code change: the slice-121 machinery
(catalog.attrDefRowsLocked sorted-key builder + dependVirtualRows) generalizes to
N columns as-is. The slice is a regression guard for the sibling-path hazard:
each column's pg_attrdef row carries a distinct oid, and each attrdef→sequence
pg_depend NORMAL link pairs with the correct sequence — a collision or crossed
pairing would cross-wire the nextval() defaults (a → mser_b_seq). Verified
byte-identical vs real pg_dump 18.3 (reference captured at /tmp/du122_pgdata).
Files: internal/testport/pgdump_connsetup_test.go (mser fixture + asserts +
cross-wire negative guards), docs/design/0110-0001-pg-dump-tap-port.md (Slice 122
section), .ralph/fix_plan.md (PROGRESS loop #86).

Next direction (slice 123): a table+sequence+VIEW dependency-ordering case (view
depends on table; pg_dump must emit CREATE TABLE before CREATE VIEW — verify
topological emission ORDER, not just presence), OR a mixed identity+serial table
stressing both deptype paths ('i' + 'a') in one dependency graph, OR an
explicit-START / non-default serial sequence (e.g. column created via ALTER TABLE
ADD COLUMN serial, or a serial with a manually-bumped sequence value).

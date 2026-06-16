(idle — nothing in flight)

Last landed: DU-002 slice 123 (loop #87) — a table mixing an IDENTITY column and
a SERIAL column on one relation (`CREATE TABLE public.mix (id integer GENERATED
ALWAYS AS IDENTITY, n serial, note text)`) dumps byte-identically vs real pg_dump
18.3. NO production code change: slice-120 (identity 'i') + slice-121 (serial 'a')
machinery compose on one relation as-is. The slice is a regression guard for the
deptype sibling-path hazard — `dependVirtualRows` must tag the identity sequence
'i' (→ embedded in ADD GENERATED … AS IDENTITY clause, no standalone CREATE
SEQUENCE/OWNED BY/SET DEFAULT) and the serial sequence 'a' (→ standalone CREATE
SEQUENCE + OWNED BY + SET DEFAULT) on the SAME table; a mis-tag flips the emitted
shape. Verified vs reference at /tmp/du123_pgdata. Gotcha caught during authoring:
the unqualified negative `ALTER COLUMN id SET DEFAULT nextval` falsely matched
ser_tbl's legitimate `id` serial default — negatives are now scoped with the
`public.mix` prefix.
Files: internal/testport/pgdump_connsetup_test.go (mix fixture + asserts +
cross-path negative guards), docs/design/0110-0001-pg-dump-tap-port.md (Slice 123
section), .ralph/fix_plan.md (PROGRESS loop #87).

Next direction (slice 124): a table+sequence+VIEW dependency-ordering case (view
depends on table; pg_dump must emit CREATE TABLE before CREATE VIEW — verify
topological emission ORDER, not just presence), OR an explicit-START / non-default
serial sequence (serial added via ALTER TABLE ADD COLUMN serial, or a serial whose
sequence value was manually bumped so setval reflects a non-1 last_value).

(idle — nothing in flight)

Last loop (#25): M0119-0004 **schema-qualified `OWNED BY` round-trip in
pg_dump** — LANDED (DU-002 slice 304). Design
`0119-0004-owned-by-schema-qualified.md`.

`CREATE SEQUENCE … OWNED BY public.owner_tbl.id` (3-part schema.table.column)
previously errored `sequence cannot be owned by relation "public"`:
`validateSeqOwnedBy` (operators_ddl.go) split the owner on the FIRST dot →
table="public" / col="owner_tbl.id". Fix = `strings.LastIndex` for the column
split (column is the LAST dotted component), mirroring the already-correct
`InMemory.dependVirtualRows` (catalog.go). The pre-existing schema-qualified
retry (`strings.Index(tblPart, ".")`) is now reached with correct
tblPart="public.owner_tbl". One-line logic change; bounded to that function.

- operators_ddl.go: validateSeqOwnedBy first-dot → last-dot column split.
- testport/pgdump_connsetup_test.go: slice 304 (`qowned_seq OWNED BY
  public.owner_tbl.label`; create + dump round-trip pinned).

Gates run: TestPort_PgDumpConnectionSetup PASS; executor sequence/identity
suite PASS; go build clean; pgbench smoke via pre-commit.

NEXT loop — remaining open under M0119-0004:
- pg_dump 002–010 catalog-view parity battery (more slices, slice-by-slice via
  TestPort_PgDumpConnectionSetup). Resume = next gap in pg_dump's getter battery.
- SERIAL nextval-default detection uses 2-part `table.column` exact match
  (`FindSequenceOwnedBy`, operators_sequence.go:266); a 3-part stored OwnedBy
  would not match — latent, only matters if a column DEFAULT nextval()s a
  schema-qualified OWNED BY sequence (not exercised by slice 304). Note for later.
- extended-protocol commit-time deferral (architecturally entangled).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).

(idle — nothing in flight)

Last landed: DU-002 slice 260 (loop #26) — a multi-subcommand `ALTER TYPE`
(`ADD ATTRIBUTE d text, DROP ATTRIBUTE b, ALTER ATTRIBUTE c TYPE numeric(12,3),
ADD ATTRIBUTE e text COLLATE "C"` in one statement) now applies every action and
round-trips through pg_dump. Slices 253/255/256 each handled one action and
stub-consumed trailing `, <subcommand>`, silently dropping the rest.

Mechanism:
- AST (ast.go): new AlterTypeAttrCmd{Kind,Name,Type,Collation,IfExists} +
  AlterTypeStmt.AttrCmds []AlterTypeAttrCmd.
- Parser (parseAlterType, ddl.go): try attribute list first via new backtracking
  parseOneAttrCmd (ok=false + p.idx restore for ADD VALUE/RENAME/OWNER); `,` loop;
  shared helpers parseAttrTypeTokens + consumeAttrCmdTrailer; mirrorFirstAttrCmd
  copies AttrCmds[0] into legacy scalar fields so single-cmd path + parser tests
  are unchanged. RENAME ATTRIBUTE stays singular (legacy RenameAttrOld).
- Executor (execAlterType → new execAlterTypeAttrCmds, operators_ddl.go): gated on
  len(AttrCmds) > 1; folds actions left-to-right into one []CompositeField reusing
  the single branches' validation/SQLSTATEs (42701/42703/42P16, IF EXISTS→NOTICE),
  one xmax-stamp + re-sync (OIDs stable). len ≤ 1 keeps the proven branches.
- No pg_dump-side change.

Files: internal/parser/ast.go (AlterTypeAttrCmd + AttrCmds), internal/parser/ddl.go
(parseAlterType rewrite + parseOneAttrCmd/parseAttrTypeTokens/consumeAttrCmdTrailer/
mirrorFirstAttrCmd), internal/parser/m0097_0017_test.go
(TestAlterTypeMultiSubcommandParsing), internal/executor/operators_ddl.go
(len>1 gate + execAlterTypeAttrCmds), internal/testport/pgdump_connsetup_test.go
(public.multi_comp fixture + contiguous-block assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 260), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; parser+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.7s); pgbench pre-commit smoke runs on commit.

Next (slice 261+): multi-level / INHERITS partition-tree dump fidelity, or a
dedicated MINVALUE/MAXVALUE keyword AST node (slice 169 deferral).

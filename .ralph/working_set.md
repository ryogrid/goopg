Loop #52 COMPLETE, ready to commit: COMMENT ON CAST type-name-synonym
re-verification (test-only, no production change). Closes the last open
item from the loop #51 DROP CAST synonym-key fix
(`castKey`/`castKeyTypeName` resolving through `TypeNameToOID` before
keying `internal/catalog.RegisterCast`/`DropCast`/`CastByTypes`).

New: internal/executor/comment_on_cast_synonym_test.go
(`TestCommentOnCastResolvesTypeNameSynonym`) — creates a cast via
`CREATE CAST (float4 AS text) WITHOUT FUNCTION`, comments on it via the
synonym spelling `COMMENT ON CAST (real AS text) IS '...'`, asserts the
description lands under pg_cast (classoid 2605) keyed on the SAME OID
`CastByTypes("float4","text")` returns. Confirms `execCompatNoop`'s
`case "cast"` handler (operators_ddl.go ~13756) inherits the loop #51 fix
with no further code change needed.

Gates run this loop: go build ./... clean; go vet executor+catalog clean;
full internal/catalog + internal/executor suites PASS; gofmt clean on new
file; make ralph-state-guard OK (self-repaired stale progress.json marker,
same recurring pattern as prior loops).

Docs: deferral_ledger.md — flipped loop #51's DROP CAST row to `resolved`
and appended a new `resolved` row for this loop's re-verification.
fix_plan.md — appended loop #52 note under M0119-0004 (after the attacl
step-2 note, before M0119-0005). Design doc
docs/design/0110-0001-pg-dump-tap-port.md — new "COMMENT ON CAST synonym
re-verification (loop #52, test-only)" subsection appended after the
loop #51 DROP CAST subsection.

Next step: commit + push, then pick the next open deferral_ledger row.
Candidates surfaced by prior loops (still open):
- ALTER COLLATION OWNER/RENAME/REFRESH VERSION (loop #50 row) — same shape
  as other unhandled ALTER-object gaps.
- attacl high-blast-radius half (loop #88 fix_plan note): parser
  `AttrACLChange` capture + `execAttrACLChange`/`resyncAttrACLHeapRow` +
  pg_attribute seqscan `attacl` decode hook + DU-002 column-GRANT
  connsetup slice.
- Builtin pg_proc rows not SQL-queryable (loop #45/TRANSFORM row) —
  systemic blocker affecting CAST/TRANSFORM/CONVERSION WITH FUNCTION on
  builtin functions; large, needs its own scoped loop.
Re-scan .ralph/deferral_ledger.md for the highest-value open "| - |" row
before picking (90+ open rows remain).

(idle — nothing in flight)

## Loop #9 (2026-09-01) result — M0134-0188 (`xml.sql`) contained fix shipped

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260901-010436`
(7 items) — all already filed (confirmed via grep against fix_plan.md: 5
pre-existing rows re-failed, 2 already filed loop #7/#8). No new items filed
this loop.

**Task:** M0134-0188 — `xml.sql`. Sized live for the first time: 0/1 PASS,
2222 diff lines, 239 `^+ERROR`, 38 `^-ERROR`. Landed the fifth "missing
evalCast arm = unvalidated text" fix (commit `db71a8d2a`), taking it to 2202
diff lines / 37 `^-ERROR`. CSV stays `failed`/`pass_required=no` (dominated
by two REFACTOR-tier subsystems — see below).

**Fix shipped:** goopg had no `evalCast` arm for `"xml"` at all, so
`'<wrong'::xml` and (worse) an *implicit* column-coercion
`INSERT INTO t(x xml) VALUES ('<wrong')` both silently succeeded. New
`xmlValidate` (`internal/executor/xmltypes.go`) checks well-formedness per
the session `xmloption` GUC (declared but previously unconsumed) via
`encoding/xml.Decoder` strict mode — a well-formedness check, not a full XML
engine. Wired into TWO sibling paths (both needed independent fixes):
`evalCast`'s new `"xml"` case (also backs `pg_input_is_valid`/
`pg_input_error_info`), and `encodeValuePGCtx`'s new `"xml"` case
(`codec.go`) — the physical row-encoder INSERT/UPDATE calls for implicit
coercion, which never routed through `evalCast`. New test
`TestXMLWellFormedness`. Regression A/B (stash-based,
`create_table`/`alter_table`/`type_sanity`) byte-identical, zero
regressions.

**Remaining gaps (ledgered, `.ralph/deferral_ledger.md` M0134-0188 row):**
(a) SQL/XML publishing-function grammar (`XMLELEMENT`/`XMLTABLE`/
`XMLCONCAT`/…) has no grammar production at all — REFACTOR-tier, same shape
as the already-filed SQL/JSON gap M0134-0168a, blocks M0134-0189
(`xmlmap.sql`) too; (b) XPath evaluation (`xpath`/`xpath_exists`) plus three
contained leaf functions (`xmlcomment`/`xmltext`/`xml_is_well_formed*`);
(c) `SET XML OPTION DOCUMENT|CONTENT`'s own grammar form; (d)
declaration-level well-formedness checks `encoding/xml` doesn't model (e.g.
`standalone="y"` validity). None attempted.

**Gates run:** `go build ./...` clean; `go test ./internal/optimizer/...
./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/...
./internal/executor/...` all PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (full suite incl. internal/initdb,
cmd/goopg); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34); `make
ralph-state-guard` PASS (one auto-repair, same benign pre-existing
"completed" marker pattern as loops #4-#8); pre-commit pgbench smoke PASS
(505/650/11723 TPS, 0 failed).

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next unclaimed
M0134 case per ordering is **M0134-0189** (`xmlmap.sql`, `not-tried`, never
sized) — expect the same sizing-and-park shape (gated by the same SQL/XML
grammar gap (a) above) unless that subsystem gets opened first.

**In-flight:** none.

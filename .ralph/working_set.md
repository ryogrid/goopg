(idle — nothing in flight)

Loop #23 COMPLETE: M0119-0004 DU-002 slice 384 — array-metachar OPTIONS value
quoting. Closes the array-metacharacter quoting deferral carried since slices
378–380 (FDW/SERVER/USER MAPPING OPTIONS).

Fix (internal/catalog/catalog.go):
- NEW quoteArrayElement(s): reproduces PG array_out element quoting
  (src/backend/utils/adt/arrayfuncs.c) — double-quotes any element with a
  comma/brace/whitespace/double-quote/backslash (and ""/NULL case-insensitively),
  backslash-escaping embedded "/\.
- NEW arrayTextLiteral(parts): wraps elements as {…}, quoting each.
- optionsArrayLiteral now delegates to arrayTextLiteral (metachar-free elements
  stay byte-identical bare; `host 'a,b'` → `{"host=a,b"}`).
- The 3 pg_class.reloptions renderers (table/toast/index) also route through
  arrayTextLiteral (their values are numeric/bool/enum so output is unchanged,
  but path is now metachar-safe).
- Round-trip works because existing parseTextArray (executor/expr.go) already
  unquotes `"host=a,b"` → `host=a,b`, which pg_options_to_table splits on first
  `=` to recover value `a,b`.

Files: internal/catalog/catalog.go (helpers + 4 render sites),
internal/catalog/fdw_registry_test.go (+TestQuoteArrayElement,
+TestOptionsArrayLiteralMetachar), internal/testport/pgdump_connsetup_test.go
(+goopg_srv_mc `OPTIONS (host 'a,b')` fixture + slice-384 assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 384 section),
.ralph/deferral_ledger.md (resolved row).

Gates: catalog pkg tests PASS; TestPort_PgDumpConnectionSetup PASS (5.4s,
byte-identical vs pg_dump 18.3); go build ./... clean; go vet catalog clean;
gofmt clean on my edits (catalog.go's 10 gofmt hunks are pre-existing
go1.25/1.26 version-mismatch noise, none touch my functions). pgbench smoke =
pre-commit hook. No TPC-H (rendering-only, no executor/planner row-path change).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: ALTER SERVER/FDW/USER
MAPPING … OPTIONS (ADD/SET/DROP) action verbs (parse + mutate Options); range
types; aggregates; operators; text-search configs; CREATE COLLATION;
external-binary keyword view-alias/collation fixture (low value).

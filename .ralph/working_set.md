Loop #42: M0118-0008 enabler (design 0118-0044) — NOT a promotion.

Made the isolation harness's `splitSQLStatements` dollar-quote aware. It
handled single-quotes + `--` comments but NOT `$$…$$`/`$tag$…$tag$`, so a
`do $$ … commit; … $$;` STEP body (plpgsql-toast) was split at the first
body-internal `;` and goopg got the truncated `do $$\n declare x text` →
false `unterminated dollar-quoted string` lex error (a harness artifact;
goopg lexes `$$` fine). New `dollarOpener` helper recognises a `$tag$`
opener (empty/identifier tag, no `$` inside, rejects `$1`); splitter tracks
an active `dollarCloser`, copies bytes verbatim until the exact closer.

Setup blocks (dollar-quoted CREATE FUNCTION) bypass the splitter
(execGlobalSetup/execConnSetupCapture) so unaffected. Only `merge-update` is
already-strict with a dollar-quoted step; its `$$` has no top-level `;` →
byte-identical under both splitters (verified strict PASS).

Result: plpgsql-toast's first divergence now points at the REAL blocker —
`unsupported PL/pgSQL statement` (COMMIT inside a DO block). Still deferred.

Files: internal/testport/framework/isolation_runner.go (splitSQLStatements +
dollarOpener); internal/testport/framework/isolation_test.go
(TestSplitSQLStatementsDollarQuote); docs/design/0118-0044-* + README index;
fix_plan M0118-0008 note; deferral ledger.

Gates: TestSplitSQLStatementsDollarQuote PASS; framework pkg PASS;
TestPort_IsolationMergeUpdate strict PASS (no regression); go vet clean;
ralph-state-guard; pgbench smoke = pre-commit hook.

Next step: M0118-0008 hard tail needs real engine work. Cheapest probed:
`vacuum-no-cleanup-lock` (add `vacuum_multixact_freeze_min_age` GUC +
reltuples/relpages accounting under a pin holder) OR parser additions
(`NOT VALID`/`VALIDATE CONSTRAINT` for alter-table-1/2). The alter-table-4 /
partition-attach/detach specs need transactional-DDL cross-session
visibility (goopg applies catalog DDL immediately) — a large subsystem.

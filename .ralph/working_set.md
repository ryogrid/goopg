(idle — nothing in flight)

Loop #15 completed and committed: closed the "bpchar/varchar typmod
truncation in the inline-cast evaluator" candidate named by loop #14's
working set. `internal/executor/expr.go`'s `*planner.CastExpr` case in
`evalExpr` (same call site as the OID-18 `"char"` fix, since `x.Typmod` is
only in scope there) now truncates a `KindString` cast result to
`x.Typmod` runes whenever `castTargetType` (post bare-char→bpchar rename)
is `varchar`/`bpchar`/`char`/`character` — silent truncation, no `22001`
(verified against real, unmodified PostgreSQL 18.3 via
`postgres/local_install`: explicit `::type(n)` casts truncate without
error, unlike assignment/INSERT coercion). Deliberately did NOT implement
bpchar/char space-padding (also real-PG behavior, verified via
`octet_length`): goopg's `Datum`/storage path (`coerceTextLikeDatum`)
already stores bpchar trimmed, not padded, and padding only the inline-cast
path would make the two paths disagree — recorded as a new, separate,
cross-cutting deferral instead. This closed a side stale-test-expectation
too: `TestCastExprCharTypmodDisambiguation`'s `SELECT 'xyz'::char` now
correctly expects `"x"` (bpchar(1) truncation), not the old unchanged
`"xyz"`. New test: `TestInlineCastVarcharBpcharTypmodTruncation`
(`internal/executor/char_oid18_truncation_test.go`). Verified: go build
clean; `go test ./internal/executor/... ./internal/planner/...
./internal/parser/...` all PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `make ralph-state-guard` OK (auto-repaired an unrelated
stale status/progress marker). Design doc
(`docs/design/0122-0005-char-oid18-disambiguation.md`) new "Follow-up:
varchar(n)/bpchar(n)/char(n) inline-cast truncation" section + README row
extended. Ledger row appended (status `-`): newly deferred is bpchar/char
right-padding of short values (needs a project-wide decision on how to
represent padded fixed-width values — a new Datum flag/kind, or padding
applied consistently at both cast-eval and storage-coercion sites plus
every comparison/length/concat call site — materially larger and
cross-cutting, not a one-branch follow-up).

Next candidate (pick ONE): the view's-own-ACL gap from M0122-0008
(materially larger — needs a preliminary per-statement RTE-style
permission pass, planning currently has no session-role visibility),
resume the M0110-0001 multi-database isolation survey (fix_plan "Current
Priority" banner — per-database catalog/storage isolation, milestone-scale,
repeatedly deferred across many loops as too large for one loop), the
bpchar/char right-padding gap just recorded (large/cross-cutting — probably
still too big for one loop; would need scoping first), or survey
`.ralph/deferral_ledger.md` for another fresh open (`status = -`) row.

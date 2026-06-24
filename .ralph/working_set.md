(idle — nothing in flight)

Loop #38 COMPLETE: M0118-0009 JSON accessor operators `->` / `->>` ADDED
(enabler, design 0118-0100 — NOT a spec promotion). Previously a hard lex error.

What landed — JSON `->` (element/field as json) + `->>` (as text):
- parser/lexer.go: greedy multi-char op match adds `->` (2-char) and promotes
  `->>` (3-char, same pattern as `!~`→`!~*`).
- parser/op.go: new OpJSONGet / OpJSONGetText; ParseBinaryOp + String() round-trip.
- parser/select.go: peekBinaryOp maps both at new precJSON=6 ("other operators"
  group, same as ||, left-associative).
- executor/expr.go: evalBinary case → evalJSONArrow: json.Decoder+UseNumber()
  (exact int round-trip), int key indexes array (neg from end), text key selects
  object field, type-mismatch/missing→SQL NULL, `->` re-encodes element as
  canonical json (json null→"null"), `->>` scalar→bare text / json null→SQL NULL
  / container→compact json. Invalid json left operand → 22P02.
- Tests: parser/json_arrow_test.go (lex + left-assoc parse chain + ->> opcode),
  executor/json_arrow_test.go (field/index/neg/OOB/mismatch/json-null/->>-text/
  invalid-json/chained).
- docs/design/0118-0100 + README index; deferral ledger + fix_plan note.

Why enabler not promotion: horizons.spec needs MUCH more. Re-probe after `->`:
first divergence advanced from the `->` lex error to plpgsql
`EXECUTE … INTO STRICT v_ret`. horizons' remaining blockers (in order):
(1) plpgsql `EXECUTE … INTO STRICT`; (2) EXPLAIN (FORMAT json) emit
`Heap Fetches` for index-only scans (operators_explain.go); (3) Effort-L MVCC
core — IOS heap-fetch count reflecting pruning + prune/VACUUM respecting a
concurrent older snapshot for permanent vs temp tables.

Probe-ranked ALL 12 remaining failed isolation specs this loop — every one is
Effort-L: intra-grant-inplace (full pg_class catalog-tuple row-lock matrix +
multixact + deadlock, 11 perms), horizons (above), index-only-bitmapscan
(DECLARE CURSOR + VACUUM TRUNCATE opt), fk-partitioned-1/2 (ATTACH PARTITION +
partitioned-FK), predicate-gin (GIN AM + intarray), predicate-gist (GiST AM +
point), stats (pg_stat_force_next_flush + cumulative stats), deadlock-parallel
(LANGUAGE internal + parallel workers), prepared-transactions{,-cic} (2PC).

Gates: parser+executor JSON-arrow units PASS; full internal/parser +
internal/executor suites PASS no regression; build+vet clean; gofmt -l only
pre-existing go1.25↔1.26 noise (op.go/lexer.go unrelated regions). pgbench smoke
= pre-commit hook. State guard repaired→consistent.

NEXT: no cheap isolation-spec promotion remains. Either keep laddering horizons
(plpgsql EXECUTE INTO STRICT next) or start intra-grant-inplace perm1 (reuse the
loop #36 / 0118-0098 GRANT-xmax-wait mechanism for the pg_class relhasindex
inplace update).

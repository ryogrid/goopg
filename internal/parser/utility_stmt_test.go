package parser

import "testing"

// TestUtilityStatements pins the P5.3 block: transaction control, prepared
// statements, cursors and the maintenance commands — 1,076 -> 586 fewer
// unrouted regress fragments (fetch 139, analyze 130, declare 80, execute 45,
// reindex 43, vacuum 33, savepoint 33, prepare 30, close 29 ...).
//
// Legacy is NARROWER than gram.y in five places and this grammar follows
// legacy, not upstream:
//
//   - DISCARD ALL is rejected (only PLANS / SEQUENCES / TEMP / TEMPORARY).
//   - a cursor takes [NO] SCROLL only; BINARY / INSENSITIVE / ASENSITIVE are
//     rejected before CURSOR.
//   - CLUSTER has no parenthesised option list, only the bare VERBOSE word.
//   - MOVE is parsed and DISCARDED as an empty CompatNoopStmt{Tag:"MOVE"}.
//   - VACUUM's bare-keyword prefix admits only VERBOSE/ANALYZE/FULL/FREEZE;
//     the longer option names exist inside the parens only.
func TestUtilityStatements(t *testing.T) {
	for _, q := range []string{

		"SAVEPOINT s1", "RELEASE SAVEPOINT s1", "RELEASE s1",
		"CHECKPOINT", "DISCARD PLANS", "DISCARD TEMP", "DISCARD TEMPORARY", "DISCARD SEQUENCES",
		"DEALLOCATE foo", "DEALLOCATE ALL", "DEALLOCATE PREPARE foo",
		"PREPARE foo AS SELECT 1", "PREPARE foo (int, text) AS SELECT $1",
		"EXECUTE foo", "EXECUTE foo(1, 'x')",
		"CLOSE foo", "CLOSE ALL",
		"DECLARE c CURSOR FOR SELECT 1",
		"DECLARE c NO SCROLL CURSOR WITHOUT HOLD FOR SELECT 1",
		"DECLARE c SCROLL CURSOR FOR SELECT 1",
		"DECLARE c CURSOR WITH HOLD FOR SELECT 1",
		"FETCH c", "FETCH ALL FROM c", "FETCH 5 IN c", "FETCH FORWARD 3 FROM c",
		"FETCH BACKWARD ALL FROM c", "FETCH NEXT FROM c", "FETCH PRIOR FROM c",
		"FETCH FIRST FROM c", "FETCH LAST FROM c", "FETCH ABSOLUTE 2 FROM c",
		"FETCH RELATIVE -1 FROM c", "FETCH FORWARD ALL IN c",
		"MOVE c", "MOVE ALL IN c", "MOVE BACKWARD 2 FROM c", "MOVE FORWARD ALL FROM c",
		"ANALYZE", "ANALYZE t", "ANALYZE t (a, b)", "ANALYZE VERBOSE t", "ANALYZE (VERBOSE, SKIP_LOCKED) t",
		"VACUUM", "VACUUM t", "VACUUM FULL FREEZE VERBOSE ANALYZE t", "VACUUM (FULL, ANALYZE) t (a)",
		"VACUUM ANALYZE t", "VACUUM (INDEX_CLEANUP FALSE, TRUNCATE FALSE) t",
		"VACUUM (PARALLEL 2) t", "VACUUM (BUFFER_USAGE_LIMIT '1MB') t", "VACUUM (PROCESS_TOAST false) t",
		"REINDEX INDEX i", "REINDEX TABLE t", "REINDEX SCHEMA s", "REINDEX DATABASE d",
		"REINDEX INDEX CONCURRENTLY i", "REINDEX (VERBOSE) TABLE t", "REINDEX SYSTEM d",
		"CLUSTER t USING i", "CLUSTER t", "CLUSTER", "CLUSTER VERBOSE t USING i",
		"LOCK t", "LOCK TABLE t IN ACCESS EXCLUSIVE MODE", "LOCK TABLE ONLY t IN SHARE MODE NOWAIT",
		"LOCK TABLE a, b IN ROW EXCLUSIVE MODE", "LOCK TABLE t IN SHARE UPDATE EXCLUSIVE MODE",
		"LOCK TABLE t IN SHARE ROW EXCLUSIVE MODE", "LOCK TABLE t IN ACCESS SHARE MODE",
		"LOCK TABLE t IN ROW SHARE MODE", "LOCK TABLE t IN EXCLUSIVE MODE",
			"FETCH FROM c", "FETCH IN c", "FETCH BACKWARD FROM c", "FETCH FORWARD FROM c",
		"PREPARE TRANSACTION 'gid1'",
		"REINDEX CONCURRENTLY INDEX i", "REINDEX TABLE IF EXISTS t",
		"VACUUM VERBOSE",
		"CLUSTER s.t USING i",
	} {
		assertParity(t, q)
	}
	// Both parsers must keep rejecting these: DISCARD ALL and the cursor
	// sensitivity words are gram.y forms legacy does not implement, and
	// accepting them here would silently widen the language.
	assertBothReject(t, "DISCARD ALL")
	assertBothReject(t, "DECLARE c BINARY CURSOR FOR SELECT 1")
	assertBothReject(t, "DECLARE c INSENSITIVE CURSOR FOR SELECT 1")
	assertBothReject(t, "DECLARE c ASENSITIVE CURSOR FOR SELECT 1")
	assertBothReject(t, "CLUSTER (VERBOSE) t USING i")
	assertBothReject(t, "EXPLAIN ANALYZE ANALYZE t")
	// ANALYZE's own option words are keywords, not a relation name.
	assertBothReject(t, "ANALYZE ANALYZE")
}

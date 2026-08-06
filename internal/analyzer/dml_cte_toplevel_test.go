package analyzer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDataModifyingCTEMustBeTopLevel pins PG 18.3's rule that a WITH list
// containing a data-modifying statement is legal only on the statement
// being executed (postgres/src/backend/parser/parse_cte.c:330-337 —
// `query->commandType != CMD_SELECT && pstate->parentParseState != NULL`
// raises ERRCODE_FEATURE_NOT_SUPPORTED / 0A000).
//
// Every case below was captured from a live PG 18.3 (the TPC-H reference
// cluster) before the check was written: goopg used to EXECUTE the nested
// forms, so `SELECT v FROM (WITH x AS (INSERT INTO dm VALUES (1)
// RETURNING a) SELECT a AS v FROM x) s` inserted a row and returned it.
// M0125-0051.
func TestDataModifyingCTEMustBeTopLevel(t *testing.T) {
	cat := newTestCatalog(t, "dm", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	})

	rejected := []struct {
		name string
		sql  string
	}{
		{
			name: "derived table",
			sql:  `SELECT v FROM (WITH x AS (INSERT INTO dm VALUES (1) RETURNING a) SELECT a AS v FROM x) s`,
		},
		{
			name: "CTE body",
			sql:  `WITH o AS (WITH x AS (INSERT INTO dm VALUES (3) RETURNING a) SELECT a FROM x) SELECT * FROM o`,
		},
		{
			name: "set-op right arm",
			sql:  `SELECT 1 AS v UNION ALL (WITH x AS (INSERT INTO dm VALUES (4) RETURNING a) SELECT a FROM x)`,
		},
		{
			// The parenthesised LEFT arm is its own query level even
			// though the statement's own WITH would be legal here —
			// PG rejects it, so stmtRoot must not survive the descent
			// into s.SetOpOperand when the chain has a right arm.
			name: "set-op parenthesised left arm",
			sql:  `(WITH x AS (INSERT INTO dm VALUES (10) RETURNING a) SELECT a FROM x) UNION ALL SELECT 99`,
		},
		{
			name: "nested UPDATE CTE",
			sql:  `SELECT v FROM (WITH x AS (UPDATE dm SET a = a RETURNING a) SELECT a AS v FROM x) s`,
		},
		{
			name: "nested DELETE CTE",
			sql:  `SELECT v FROM (WITH x AS (DELETE FROM dm WHERE a = 1 RETURNING a) SELECT a AS v FROM x) s`,
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := analyzeWithCat(t, tc.sql, cat)
			if err == nil {
				t.Fatalf("analyze(%q): got nil, want 0A000", tc.sql)
			}
			ae, ok := err.(*AnalyzeError)
			if !ok {
				t.Fatalf("analyze(%q): got %T %v, want *AnalyzeError", tc.sql, err, err)
			}
			if ae.Code != "0A000" {
				t.Fatalf("analyze(%q): code = %s, want 0A000", tc.sql, ae.Code)
			}
			const want = "WITH clause containing a data-modifying statement must be at the top level"
			if !strings.Contains(ae.Message, want) {
				t.Fatalf("analyze(%q): message = %q, want it to contain %q", tc.sql, ae.Message, want)
			}
		})
	}

	// The accepted half matters just as much: the check must not close
	// the door on the legal top-level forms, all of which PG executes.
	accepted := []struct {
		name string
		sql  string
	}{
		{name: "top-level SELECT", sql: `WITH x AS (INSERT INTO dm VALUES (5) RETURNING a) SELECT * FROM x`},
		{name: "top-level set-op statement", sql: `WITH x AS (INSERT INTO dm VALUES (6) RETURNING a) SELECT a FROM x UNION ALL SELECT 99`},
		{name: "top-level INSERT", sql: `WITH x AS (INSERT INTO dm VALUES (7) RETURNING a) INSERT INTO dm SELECT a + 100 FROM x`},
		{name: "top-level DELETE", sql: `WITH x AS (INSERT INTO dm VALUES (15) RETURNING a) DELETE FROM dm WHERE a = 15 RETURNING a`},
		{name: "top-level UPDATE", sql: `WITH x AS (INSERT INTO dm VALUES (16) RETURNING a) UPDATE dm SET a = a + 1 WHERE a = 16`},
		// A parenthesised whole statement adds no parse-state level in
		// PG's grammar (select_with_parens), so it stays top level.
		{name: "top-level parenthesised", sql: `(WITH x AS (INSERT INTO dm VALUES (8) RETURNING a) SELECT a FROM x)`},
		// Non-DML CTEs nest freely — the check must key on DMLBody, not
		// on nesting alone.
		{name: "nested plain CTE", sql: `SELECT v FROM (WITH x AS (SELECT 1 AS v) SELECT v FROM x) s`},
		{name: "plain CTE inside a CTE body", sql: `WITH o AS (WITH i AS (SELECT 2 AS v) SELECT v FROM i) SELECT * FROM o`},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := analyzeWithCat(t, tc.sql, cat); err != nil {
				t.Fatalf("analyze(%q): got %v, want nil", tc.sql, err)
			}
		})
	}
}

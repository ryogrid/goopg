package parser

import "testing"

// TestAlterSystemParity pins ALTER SYSTEM's forms (gram.y AlterSystemStmt,
// :11593) — SET with =/TO, a list value, DEFAULT, RESET name, RESET ALL and a
// custom dotted name. Before M0122-0008 the hand-written compat scanner
// swallowed every form into a CompatNoopStmt, so ALTER SYSTEM reported
// success and changed nothing.
func TestAlterSystemParity(t *testing.T) {
	for _, q := range []string{
		"ALTER SYSTEM SET work_mem = '64MB'",
		"ALTER SYSTEM SET work_mem TO 1024",
		"ALTER SYSTEM SET search_path TO a, b",
		"ALTER SYSTEM SET work_mem TO DEFAULT",
		"ALTER SYSTEM SET work_mem = DEFAULT",
		"ALTER SYSTEM RESET work_mem",
		"ALTER SYSTEM RESET ALL",
		"ALTER SYSTEM SET my.custom = 1",
	} {
		assertParity(t, q)
	}
	for _, q := range []string{
		"ALTER SYSTEM SET work_mem",
		"ALTER SYSTEM RESET",
	} {
		assertBothReject(t, q)
	}
}

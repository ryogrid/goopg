package server

import (
	"testing"
)

// TestClassifyDatabaseDDL pins the M0054-0001 string-prefix matcher
// against the shapes HammerDB and `psql` issue.
func TestClassifyDatabaseDDL(t *testing.T) {
	cases := []struct {
		sql      string
		wantKind databaseDDLKind
		wantName string
	}{
		{"CREATE DATABASE tpch", databaseDDLCreate, "tpch"},
		{"create database tpch;", databaseDDLCreate, "tpch"},
		{"  CREATE DATABASE  Foo  ", databaseDDLCreate, "Foo"},
		{`CREATE DATABASE "Mixed Case"`, databaseDDLCreate, "Mixed Case"},
		{"CREATE DATABASE tpch OWNER tpch", databaseDDLCreate, "tpch"},
		{"DROP DATABASE tpch", databaseDDLDrop, "tpch"},
		{"DROP DATABASE IF EXISTS tpch", databaseDDLDrop, "tpch"},
		{"drop database if exists scratch;", databaseDDLDrop, "scratch"},
		// negatives
		{"CREATE TABLE t (a int)", databaseDDLNone, ""},
		{"SELECT 1", databaseDDLNone, ""},
		{"", databaseDDLNone, ""},
	}
	for _, c := range cases {
		gotKind, gotName := classifyDatabaseDDL(c.sql)
		if gotKind != c.wantKind || gotName != c.wantName {
			t.Errorf("classifyDatabaseDDL(%q) = (%d, %q), want (%d, %q)",
				c.sql, gotKind, gotName, c.wantKind, c.wantName)
		}
	}
}

// TestExtractFirstIdentifier pins the lex helper's behaviour for the
// shapes M0054-0001 actually sees in the wild — bare identifiers and
// double-quoted ones with embedded whitespace.
func TestExtractFirstIdentifier(t *testing.T) {
	cases := map[string]string{
		"tpch":             "tpch",
		"tpch OWNER tpch":  "tpch",
		`"Mixed Case"`:     "Mixed Case",
		`"Mixed" SOMETHING`: "Mixed",
		"tpch;":            "tpch",
		"tpch,more":        "tpch",
		"":                 "",
		"   ":              "",
	}
	for in, want := range cases {
		if got := extractFirstIdentifier(in); got != want {
			t.Errorf("extractFirstIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDatabaseDDLCommandTag pins the wire-protocol tag returned for
// each kind. The bare empty string covers the negative case so the
// dispatch path can fall through cleanly.
func TestDatabaseDDLCommandTag(t *testing.T) {
	cases := map[string]string{
		"CREATE DATABASE tpch":      "CREATE DATABASE",
		"DROP DATABASE tpch":        "DROP DATABASE",
		"DROP DATABASE IF EXISTS x": "DROP DATABASE",
		"SELECT 1":                  "",
	}
	for sql, want := range cases {
		if got := databaseDDLCommandTag(sql); got != want {
			t.Errorf("databaseDDLCommandTag(%q) = %q, want %q", sql, got, want)
		}
	}
}

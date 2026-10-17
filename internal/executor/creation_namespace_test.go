package executor

import (
	"errors"
	"testing"
)

// TestCreateInMissingSchemaRaises3F000 pins RangeVarGetCreationNamespace /
// QualifiedNameGetCreationNamespace (namespace.c): every statement that
// creates a schema-qualified object needs the schema to exist —
// get_namespace_oid's 3F000. goopg accepted all of these and filed the object
// under public's OID (M0119-0006). Plain CREATE TABLE carries PG's caret at
// the relation name (0-based 13 here); the others carry none. A TEMP table
// in a missing schema reports the schema, not the 0A000 temp-schema check,
// and ALTER TABLE ... SET SCHEMA resolves the relation before the schema.
func TestCreateInMissingSchemaRaises3F000(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, c := range []struct {
		sql string
		pos int
	}{
		{`CREATE TABLE nosuch.t (a int)`, 13},
		{`CREATE TABLE IF NOT EXISTS nosuch.t (a int)`, 27},
		{`CREATE TEMP TABLE nosuch.tt (a int)`, 18},
		{`CREATE TABLE nosuch.c AS SELECT 1 AS a`, 0},
		{`SELECT 1 AS a INTO nosuch.si`, 0},
		{`CREATE VIEW nosuch.v AS SELECT 1`, 0},
		{`CREATE SEQUENCE nosuch.s`, 0},
		{`CREATE MATERIALIZED VIEW nosuch.mv AS SELECT 1`, 0},
		{`CREATE TYPE nosuch.ty AS (a int)`, 0},
		{`CREATE DOMAIN nosuch.d AS int`, 0},
		{`CREATE FUNCTION nosuch.f() RETURNS int LANGUAGE sql AS 'select 1'`, 0},
	} {
		err := runDDL(t, ctx, c.sql)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != "3F000" || ee.Message != `schema "nosuch" does not exist` || ee.Pos != c.pos {
			t.Errorf("%s: err = %v (%+v); want 3F000 schema \"nosuch\" does not exist at %d", c.sql, err, ee, c.pos)
		}
	}

	// The checks pass through an existing schema and pg_temp.
	for _, sql := range []string{
		`CREATE SCHEMA here`,
		`CREATE TABLE here.t (a int)`,
		`CREATE VIEW here.v AS SELECT 1`,
		`CREATE TABLE pg_temp.tmp (a int)`,
		`CREATE TABLE public.ok (a int)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	err := runDDL(t, ctx, `ALTER TABLE public.ok SET SCHEMA nosuch`)
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "3F000" {
		t.Errorf("ALTER TABLE ... SET SCHEMA nosuch: %v; want 3F000", err)
	}
	err = runDDL(t, ctx, `ALTER TABLE public.missing SET SCHEMA nosuch`)
	if !errors.As(err, &ee) || ee.Code != "42P01" {
		t.Errorf("ALTER TABLE missing SET SCHEMA nosuch: %v; want 42P01 (relation first)", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE public.ok SET SCHEMA here`); err != nil {
		t.Errorf("ALTER TABLE ... SET SCHEMA here: %v", err)
	}
}

// TestCreateSchemaQuotedIfNotExistsDuplicate pins CreateSchemaCommand's
// front half: a quoted name is the schema's name (the compat scanner dropped
// it, so the schema was never registered — invisible until the 3F000 check
// above made objects in it fail), IF NOT EXISTS is not a schema named "if",
// and an existing name is 42P06 — or a NOTICE under IF NOT EXISTS.
func TestCreateSchemaQuotedIfNotExistsDuplicate(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, sql := range []string{
		`CREATE SCHEMA "my.schema"`,
		`CREATE FUNCTION "my.schema"."dot fn"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`,
		`CREATE SCHEMA IF NOT EXISTS ine1`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if !cat.SchemaExists("ine1") || cat.SchemaExists("if") {
		t.Fatalf("IF NOT EXISTS: ine1=%v if=%v; want true, false", cat.SchemaExists("ine1"), cat.SchemaExists("if"))
	}
	ctx.Notices = nil
	if err := runDDL(t, ctx, `CREATE SCHEMA IF NOT EXISTS ine1`); err != nil {
		t.Fatalf("second IF NOT EXISTS: %v", err)
	}
	if len(ctx.Notices) != 1 || ctx.Notices[0] != `schema "ine1" already exists, skipping` {
		t.Errorf("second IF NOT EXISTS notices = %q", ctx.Notices)
	}
	err := runDDL(t, ctx, `CREATE SCHEMA ine1`)
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "42P06" || ee.Message != `schema "ine1" already exists` {
		t.Errorf("duplicate CREATE SCHEMA: %v; want 42P06 schema \"ine1\" already exists", err)
	}
}

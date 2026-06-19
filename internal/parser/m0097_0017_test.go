package parser_test

import (
	"github.com/goopg/goopg/internal/parser"
	"testing"
)

func TestM0097_0017_EnumDomainParsing(t *testing.T) {
	tests := []string{
		`CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`,
		`CREATE TYPE planets AS ENUM ('venus', 'earth', 'mars')`,
		`ALTER TYPE planets ADD VALUE 'uranus'`,
		`ALTER TYPE planets ADD VALUE IF NOT EXISTS 'mercury'`,
		`ALTER TYPE planets ADD VALUE 'mercury' BEFORE 'venus'`,
		`ALTER TYPE planets ADD VALUE 'neptune' AFTER 'uranus'`,
		`DROP TYPE rainbow`,
		`DROP TYPE rainbow CASCADE`,
		`CREATE DOMAIN domaindroptest int4`,
		`CREATE DOMAIN domainvarchar varchar(5)`,
		`CREATE DOMAIN domainnumeric numeric(8,2)`,
		`CREATE DOMAIN domainint4 int4`,
		`CREATE DOMAIN domaintext text`,
		`CREATE DOMAIN d_notnull AS int4 NOT NULL`,
		`DROP DOMAIN domaindroptest`,
		`DROP DOMAIN domaindroptest CASCADE`,
		`DROP DOMAIN domaindroptest RESTRICT`,
	}
	for _, sql := range tests {
		t.Run(sql[:min(60, len(sql))], func(t *testing.T) {
			_, err := parser.Parse(sql)
			if err != nil {
				t.Errorf("Parse(%q) error: %v", sql, err)
			}
		})
	}
}

// TestCompositeFieldTypmodParsing covers DU-002 slice 247: a composite-type
// field whose type carries a typmod (numeric(10,2), varchar(8)) must parse —
// the inner ',' / ')' of the typmod must NOT prematurely terminate the field.
// The collected ColType is the parser's space-joined token form, which
// executor.parseCompositeFieldType decodes back into base type + atttypmod.
func TestCompositeFieldTypmodParsing(t *testing.T) {
	tests := []struct {
		sql        string
		wantFields []struct{ name, colType string }
	}{
		{
			sql: `CREATE TYPE money_amt AS (amount numeric(10,2), code varchar(8))`,
			wantFields: []struct{ name, colType string }{
				{"amount", "numeric ( 10 , 2 )"},
				{"code", "varchar ( 8 )"},
			},
		},
		{
			sql: `CREATE TYPE mixed_t AS (id int, amount numeric(10,2), label text)`,
			wantFields: []struct{ name, colType string }{
				{"id", "int"},
				{"amount", "numeric ( 10 , 2 )"},
				{"label", "text"},
			},
		},
		{
			sql: `CREATE TYPE arr_typmod AS (amounts numeric(10,2)[])`,
			wantFields: []struct{ name, colType string }{
				{"amounts", "numeric ( 10 , 2 ) [ ]"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.sql[:min(60, len(tc.sql))], func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			ct, ok := stmts[0].(*parser.CreateTypeStmt)
			if !ok {
				t.Fatalf("expected *CreateTypeStmt, got %T", stmts[0])
			}
			if !ct.IsComposite {
				t.Fatalf("expected IsComposite=true")
			}
			if len(ct.CompositeFields) != len(tc.wantFields) {
				t.Fatalf("got %d fields, want %d: %+v",
					len(ct.CompositeFields), len(tc.wantFields), ct.CompositeFields)
			}
			for i, want := range tc.wantFields {
				got := ct.CompositeFields[i]
				if got.Name != want.name || got.ColType != want.colType {
					t.Errorf("field[%d] = {%q, %q}, want {%q, %q}",
						i, got.Name, got.ColType, want.name, want.colType)
				}
			}
		})
	}
}

// TestAlterTypeAddAttributeParsing covers DU-002 slice 253: ALTER TYPE … ADD
// ATTRIBUTE col type parses into AlterTypeStmt.AddAttrName / AddAttrType with
// the same space-joined type-token form as a composite field (typmod parens and
// the `[]` array suffix survive intact). A bare `ADD ATTRIBUTE` must not be
// misread as the enum `ADD VALUE` branch.
func TestAlterTypeAddAttributeParsing(t *testing.T) {
	tests := []struct {
		sql      string
		wantName string
		wantType string
	}{
		{`ALTER TYPE addr ADD ATTRIBUTE zip text`, "zip", "text"},
		{`ALTER TYPE addr ADD ATTRIBUTE amount numeric(10,2)`, "amount", "numeric ( 10 , 2 )"},
		{`ALTER TYPE addr ADD ATTRIBUTE tags text[]`, "tags", "text [ ]"},
	}
	for _, tc := range tests {
		t.Run(tc.sql[:min(60, len(tc.sql))], func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			at, ok := stmts[0].(*parser.AlterTypeStmt)
			if !ok {
				t.Fatalf("expected *AlterTypeStmt, got %T", stmts[0])
			}
			if at.AddValue != "" {
				t.Errorf("AddValue = %q, want empty (must not take the ADD VALUE branch)", at.AddValue)
			}
			if at.AddAttrName != tc.wantName || at.AddAttrType != tc.wantType {
				t.Errorf("ADD ATTRIBUTE = {%q, %q}, want {%q, %q}",
					at.AddAttrName, at.AddAttrType, tc.wantName, tc.wantType)
			}
		})
	}
}

// TestAlterTypeRenameAttributeParsing covers the RENAME ATTRIBUTE old TO new
// sub-branch (DU-002 slice 254). It must NOT take the RENAME VALUE / RENAME TO
// paths — those carry RenameOldValue / RenameTo, not RenameAttrOld/New.
func TestAlterTypeRenameAttributeParsing(t *testing.T) {
	tests := []struct {
		sql     string
		wantOld string
		wantNew string
	}{
		{`ALTER TYPE addr RENAME ATTRIBUTE zip TO postal`, "zip", "postal"},
		{`ALTER TYPE addr RENAME ATTRIBUTE a TO b CASCADE`, "a", "b"},
	}
	for _, tc := range tests {
		t.Run(tc.sql[:min(60, len(tc.sql))], func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			at, ok := stmts[0].(*parser.AlterTypeStmt)
			if !ok {
				t.Fatalf("expected *AlterTypeStmt, got %T", stmts[0])
			}
			if at.RenameOldValue != "" || at.RenameTo != "" {
				t.Errorf("took a RENAME VALUE/TO branch: RenameOldValue=%q RenameTo=%q", at.RenameOldValue, at.RenameTo)
			}
			if at.RenameAttrOld != tc.wantOld || at.RenameAttrNew != tc.wantNew {
				t.Errorf("RENAME ATTRIBUTE = {%q, %q}, want {%q, %q}",
					at.RenameAttrOld, at.RenameAttrNew, tc.wantOld, tc.wantNew)
			}
		})
	}
}

// TestAlterTypeDropAttributeParsing covers the DROP ATTRIBUTE [IF EXISTS] attname
// sub-branch (DU-002 slice 255). It must NOT touch the ADD/RENAME attribute fields.
func TestAlterTypeDropAttributeParsing(t *testing.T) {
	tests := []struct {
		sql          string
		wantName     string
		wantIfExists bool
	}{
		{`ALTER TYPE addr DROP ATTRIBUTE zip`, "zip", false},
		{`ALTER TYPE addr DROP ATTRIBUTE zip CASCADE`, "zip", false},
		{`ALTER TYPE addr DROP ATTRIBUTE IF EXISTS zip`, "zip", true},
	}
	for _, tc := range tests {
		t.Run(tc.sql[:min(60, len(tc.sql))], func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			at, ok := stmts[0].(*parser.AlterTypeStmt)
			if !ok {
				t.Fatalf("expected *AlterTypeStmt, got %T", stmts[0])
			}
			if at.AddAttrName != "" || at.RenameAttrOld != "" {
				t.Errorf("took an ADD/RENAME attribute branch: AddAttrName=%q RenameAttrOld=%q", at.AddAttrName, at.RenameAttrOld)
			}
			if at.DropAttrName != tc.wantName || at.DropAttrIfExists != tc.wantIfExists {
				t.Errorf("DROP ATTRIBUTE = {%q, %v}, want {%q, %v}",
					at.DropAttrName, at.DropAttrIfExists, tc.wantName, tc.wantIfExists)
			}
		})
	}
}

// TestAlterTypeAlterAttributeParsing covers DU-002 slice 256: ALTER TYPE …
// ALTER ATTRIBUTE attname [SET DATA] TYPE newtype [COLLATE/USING/CASCADE/RESTRICT].
// The type tokens are paren-tracked (so numeric(12,3) survives) and the trailing
// COLLATE/USING/behavior clause is stub-consumed (not folded into the type).
func TestAlterTypeAlterAttributeParsing(t *testing.T) {
	tests := []struct {
		sql      string
		wantName string
		wantType string
	}{
		{`ALTER TYPE alt_comp ALTER ATTRIBUTE a TYPE bigint`, "a", "bigint"},
		{`ALTER TYPE alt_comp ALTER ATTRIBUTE a SET DATA TYPE bigint`, "a", "bigint"},
		{`ALTER TYPE alt_comp ALTER ATTRIBUTE b TYPE numeric(12,3)`, "b", "numeric ( 12 , 3 )"},
		{`ALTER TYPE alt_comp ALTER ATTRIBUTE b TYPE text COLLATE "C"`, "b", "text"},
		{`ALTER TYPE alt_comp ALTER ATTRIBUTE b TYPE varchar(64) CASCADE`, "b", "varchar ( 64 )"},
	}
	for _, tc := range tests {
		t.Run(tc.sql[:min(60, len(tc.sql))], func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			at, ok := stmts[0].(*parser.AlterTypeStmt)
			if !ok {
				t.Fatalf("expected *AlterTypeStmt, got %T", stmts[0])
			}
			if at.AddAttrName != "" || at.RenameAttrOld != "" || at.DropAttrName != "" {
				t.Errorf("took an ADD/RENAME/DROP attribute branch: AddAttrName=%q RenameAttrOld=%q DropAttrName=%q",
					at.AddAttrName, at.RenameAttrOld, at.DropAttrName)
			}
			if at.AlterAttrName != tc.wantName || at.AlterAttrType != tc.wantType {
				t.Errorf("ALTER ATTRIBUTE = {%q, %q}, want {%q, %q}",
					at.AlterAttrName, at.AlterAttrType, tc.wantName, tc.wantType)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

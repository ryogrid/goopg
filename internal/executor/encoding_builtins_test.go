package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestEvalPgClientEncoding verifies that pg_client_encoding() returns the
// current GUC value for client_encoding, defaulting to UTF8 when the setting
// is absent. M0122-0008.
func TestEvalPgClientEncoding(t *testing.T) {
	// Default: no GetSetting → UTF8.
	ctx := &Context{}
	d, err := evalPgClientEncoding(ctx)
	if err != nil {
		t.Fatalf("evalPgClientEncoding: %v", err)
	}
	if got := d.StringValue(); got != "UTF8" {
		t.Errorf("default client_encoding = %q, want UTF8", got)
	}

	// Custom: GetSetting returns LATIN1.
	ctx2 := &Context{
		GetSetting: func(name string) (string, bool) {
			if name == "client_encoding" {
				return "LATIN1", true
			}
			return "", false
		},
	}
	d, err = evalPgClientEncoding(ctx2)
	if err != nil {
		t.Fatalf("evalPgClientEncoding with GetSetting: %v", err)
	}
	if got := d.StringValue(); got != "LATIN1" {
		t.Errorf("client_encoding with GetSetting = %q, want LATIN1", got)
	}

	// Nil context → UTF8 fallback.
	d, err = evalPgClientEncoding(nil)
	if err != nil {
		t.Fatalf("evalPgClientEncoding nil ctx: %v", err)
	}
	if got := d.StringValue(); got != "UTF8" {
		t.Errorf("nil ctx client_encoding = %q, want UTF8", got)
	}
}

// TestEvalGetDatabaseEncoding verifies that getdatabaseencoding() returns the
// database encoding from the in-memory catalog. M0122-0008.
func TestEvalGetDatabaseEncoding(t *testing.T) {
	// Catalog with encoding set.
	cat := catalog.NewInMemory()
	cat.SetDatabaseEncoding("postgres", 6) // PG_UTF8
	ctx := &Context{
		Catalog:         cat,
		CurrentDatabase: "postgres",
	}
	d, err := evalGetDatabaseEncoding(ctx)
	if err != nil {
		t.Fatalf("evalGetDatabaseEncoding: %v", err)
	}
	if got := d.StringValue(); got != "UTF8" {
		t.Errorf("database encoding = %q, want UTF8", got)
	}

	// Non-default encoding: register mydb first, then set its encoding.
	cat.CreateDatabase("mydb", 10) // owner = bootstrap superuser
	cat.SetDatabaseEncoding("mydb", 8) // PG_LATIN1
	ctx.CurrentDatabase = "mydb"
	d, err = evalGetDatabaseEncoding(ctx)
	if err != nil {
		t.Fatalf("evalGetDatabaseEncoding LATIN1: %v", err)
	}
	if got := d.StringValue(); got != "LATIN1" {
		t.Errorf("database encoding = %q, want LATIN1", got)
	}

	// Nil context → UTF8 fallback.
	d, err = evalGetDatabaseEncoding(nil)
	if err != nil {
		t.Fatalf("evalGetDatabaseEncoding nil ctx: %v", err)
	}
	if got := d.StringValue(); got != "UTF8" {
		t.Errorf("nil ctx database encoding = %q, want UTF8", got)
	}

	// Empty database name → postgres default.
	ctx2 := &Context{
		Catalog:         cat,
		CurrentDatabase: "",
	}
	cat.SetDatabaseEncoding("postgres", 6)
	d, err = evalGetDatabaseEncoding(ctx2)
	if err != nil {
		t.Fatalf("evalGetDatabaseEncoding empty db: %v", err)
	}
	if got := d.StringValue(); got != "UTF8" {
		t.Errorf("empty db encoding = %q, want UTF8", got)
	}
}

// TestEvalPgCharToEncoding verifies that pg_char_to_encoding(name) resolves
// encoding names to their pg_enc integer IDs through the evalFuncCall dispatch.
// M0122-0008.
func TestEvalPgCharToEncoding(t *testing.T) {
	ctx := &Context{}

	t.Run("canonical name UTF8", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "UTF8"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != 6 {
			t.Errorf("pg_char_to_encoding('UTF8') = %d, want 6", got.Int)
		}
	})

	t.Run("alias unicode to UTF8", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "unicode"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != 6 {
			t.Errorf("pg_char_to_encoding('unicode') = %d, want 6 (UTF8)", got.Int)
		}
	})

	t.Run("canonical LATIN1", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "LATIN1"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != 8 {
			t.Errorf("pg_char_to_encoding('LATIN1') = %d, want 8", got.Int)
		}
	})

	t.Run("NULL returns NULL", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.NullConst{}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if !got.IsNull() {
			t.Errorf("pg_char_to_encoding(NULL) should return NULL, got %#v", got)
		}
	})

	t.Run("unknown encoding returns -1", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "nonexistent_encoding_xyz"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != -1 {
			t.Errorf("pg_char_to_encoding('nonexistent') = %d, want -1", got.Int)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "utf8"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != 6 {
			t.Errorf("pg_char_to_encoding('utf8') = %d, want 6", got.Int)
		}
	})

	t.Run("punctuation variant UTF-8", func(t *testing.T) {
		call := &optimizer.FuncCall{Name: "pg_char_to_encoding", Args: []optimizer.Expr{&optimizer.StringConst{Value: "UTF-8"}}}
		got, err := evalFuncCall(call, nil, ctx)
		if err != nil {
			t.Fatalf("evalFuncCall: %v", err)
		}
		if got.Int != 6 {
			t.Errorf("pg_char_to_encoding('UTF-8') = %d, want 6 (UTF8)", got.Int)
		}
	})
}

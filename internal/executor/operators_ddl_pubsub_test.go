package executor

// operators_ddl_pubsub_test.go — `execCreatePublication` canonicalises
// table references to the qualified form the walsender's publication
// filter compares against at decode time. Pins rung-10 of
// M0103-0008. See `docs/design/0103-0015-publication-table-canonicalization.md`.

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreatePublicationStoresCanonicalQualifiedName covers the load-bearing
// case: a `public.t` table referenced with an unqualified
// `CREATE PUBLICATION p FOR TABLE t` must be stored as `"public.t"` so the
// walsender's `publicationFilter.byTable["public.t"]` lookup matches.
func TestCreatePublicationStoresCanonicalQualifiedName(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	// Seed a `public.t` table on top of the fixture's bare `items` —
	// the fixture's catalog already has Schema="" entries so the
	// canonicalisation path exercised here is the public-fallback
	// branch with a real schema-qualified target.
	if _, err := ctx.Catalog.(*catalog.InMemory).CreateTable(
		parser.ObjectName{Schema: "public", Name: "t"},
		[]catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
			{Name: "v", Type: catalog.Type{Name: "text"}},
		},
	); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE t"); err != nil {
		t.Fatalf("runDDL: %v", err)
	}

	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if got, want := pub.Tables, []string{"public.t"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("pub.Tables = %v, want %v", got, want)
	}
}

// TestCreatePublicationExplicitSchemaName: when the user writes the
// schema themselves the stored canonical form must round-trip unchanged.
func TestCreatePublicationExplicitSchemaName(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if _, err := ctx.Catalog.(*catalog.InMemory).CreateTable(
		parser.ObjectName{Schema: "public", Name: "items_q"},
		[]catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		},
	); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE public.items_q"); err != nil {
		t.Fatalf("runDDL: %v", err)
	}

	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if got, want := pub.Tables, []string{"public.items_q"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("pub.Tables = %v, want %v", got, want)
	}
}

// TestCreatePublicationUnknownTableErrors: a publication referencing a
// non-existent relation must fail with `42P01` at DDL time rather than
// silently store a dead name that will never match any decoded change.
func TestCreatePublicationUnknownTableErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE nosuchtable")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42P01" {
		t.Errorf("ExecError.Code = %q, want \"42P01\"", ee.Code)
	}
}

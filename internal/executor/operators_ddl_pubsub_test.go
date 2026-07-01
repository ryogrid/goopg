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

// TestCreatePublicationOwnerDefaultsToBootstrapSuperuser: with no SET
// ROLE/SET SESSION AUTHORIZATION in effect, a freshly created publication is
// still owned by the bootstrap superuser (OID 10) — the pre-existing
// behavior, pinned so the loop #65 owner-tracking fix below doesn't regress
// the common (superuser session) case.
func TestCreatePublicationOwnerDefaultsToBootstrapSuperuser(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatalf("runDDL: %v", err)
	}
	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if pub.Owner != 10 {
		t.Errorf("pub.Owner = %d, want 10 (bootstrap superuser)", pub.Owner)
	}
}

// TestCreatePublicationOwnerTracksEffectiveRole: a publication created while
// SET ROLE has switched the session to a non-bootstrap role must be owned
// by that role's OID, not the hardcoded bootstrap superuser — matching
// PostgreSQL's GetUserId()-as-owner convention (CreatePublication,
// publicationcmds.c). DU-002 slice 424 (closes the loop #60/#63/#64 ledger
// rows' "Publication.Owner non-bootstrap-role tracking" deferral).
func TestCreatePublicationOwnerTracksEffectiveRole(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("app_owner")
	roleOID, ok := im.RoleOID("app_owner")
	if !ok {
		t.Fatal("app_owner not registered")
	}

	ctx.NonSuperuserRole = "app_owner"
	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatalf("runDDL: %v", err)
	}
	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if pub.Owner != roleOID {
		t.Errorf("pub.Owner = %d, want %d (app_owner's OID)", pub.Owner, roleOID)
	}
}

// TestCreateSubscriptionOwnerTracksEffectiveRole is
// TestCreatePublicationOwnerTracksEffectiveRole's CREATE SUBSCRIPTION
// counterpart: pg_subscription.subowner must reflect the effective role too.
func TestCreateSubscriptionOwnerTracksEffectiveRole(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("sub_owner")
	roleOID, ok := im.RoleOID("sub_owner")
	if !ok {
		t.Fatal("sub_owner not registered")
	}

	ctx.NonSuperuserRole = "sub_owner"
	sql := "CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (enabled = false)"
	if err := runDDL(t, ctx, sql); err != nil {
		t.Fatalf("runDDL: %v", err)
	}
	sub, ok := ctx.PubSub.LookupSubscription("s")
	if !ok {
		t.Fatal("subscription s not registered")
	}
	if sub.Owner != roleOID {
		t.Errorf("sub.Owner = %d, want %d (sub_owner's OID)", sub.Owner, roleOID)
	}
}

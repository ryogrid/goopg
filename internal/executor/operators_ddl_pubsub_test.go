package executor

// operators_ddl_pubsub_test.go — `execCreatePublication` canonicalises
// table references to the qualified form the walsender's publication
// filter compares against at decode time. Pins rung-10 of
// M0103-0008. See `docs/design/0103-0015-publication-table-canonicalization.md`.

import (
	"errors"
	"fmt"
	"strings"
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

// TestCreateSubscriptionRoutesToConnectionRealDBOid exercises M0122-0007
// 4d-ii-part-2b item 1's catalog+executor half: a connection's real dbOid
// (ctx.CurrentDatabaseOid) must land on the created Subscription's DBOid
// field, translated through catalog.NamespaceDBOid the same way every
// other DDL write entry point already does (items 1-3). The apply
// launcher's DefaultLaunchApplyWorker reads this field
// (applyWorkerCatalog) to seed the worker's catalog.SearchPathCatalog —
// see TestApplyWorkerAppliesInsertUnderDistinctSubscriptionDBOid
// (internal/executor/applyworker_test.go) for the consumer side.
func TestCreateSubscriptionRoutesToConnectionRealDBOid(t *testing.T) {
	const otherDBOid = 7373
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	ctx.CurrentDatabaseOid = otherDBOid

	sql := "CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (enabled = false)"
	if err := runDDL(t, ctx, sql); err != nil {
		t.Fatalf("runDDL: %v", err)
	}
	sub, ok := ctx.PubSub.LookupSubscription("s", otherDBOid)
	if !ok {
		t.Fatal("subscription s not registered")
	}
	if sub.DBOid != otherDBOid {
		t.Errorf("sub.DBOid = %d, want %d", sub.DBOid, otherDBOid)
	}
}

// TestCreateSubscriptionPostgresConnectionStaysOnDefaultDBOid pins the
// postgres/DefaultDBOid dual-mirror shim (catalog.NamespaceDBOid) on
// CREATE SUBSCRIPTION's write side, mirroring
// TestExecCreateTablePostgresConnectionStaysOnDefaultDBOid
// (ddl_write_dbid_routing_test.go) for tables.
func TestCreateSubscriptionPostgresConnectionStaysOnDefaultDBOid(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	ctx.CurrentDatabaseOid = catalog.PostgresDBOid

	sql := "CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (enabled = false)"
	if err := runDDL(t, ctx, sql); err != nil {
		t.Fatalf("runDDL: %v", err)
	}
	sub, ok := ctx.PubSub.LookupSubscription("s")
	if !ok {
		t.Fatal("subscription s not registered")
	}
	if sub.DBOid != catalog.DefaultDBOid {
		t.Errorf("sub.DBOid = %d, want DefaultDBOid (%d)", sub.DBOid, catalog.DefaultDBOid)
	}
}

// TestAlterPublicationOwnerTo covers the loop #65 ledger row's "ALTER
// PUBLICATION ... OWNER TO" resume point: a publication's owner can be
// reassigned after CREATE, matching PostgreSQL's AlterPublicationOwner
// (publicationcmds.c).
func TestAlterPublicationOwnerTo(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("new_owner")
	roleOID, ok := im.RoleOID("new_owner")
	if !ok {
		t.Fatal("new_owner not registered")
	}

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatalf("runDDL create: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER PUBLICATION p OWNER TO new_owner"); err != nil {
		t.Fatalf("runDDL alter owner: %v", err)
	}
	pub, ok := ctx.PubSub.LookupPublication("p")
	if !ok {
		t.Fatal("publication p not registered")
	}
	if pub.Owner != roleOID {
		t.Errorf("pub.Owner = %d, want %d (new_owner's OID)", pub.Owner, roleOID)
	}
}

// TestAlterPublicationOwnerToUnknownRoleErrors: reassigning to a role that
// doesn't exist must fail with 42704, not silently mint a dangling OID.
func TestAlterPublicationOwnerToUnknownRoleErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatalf("runDDL create: %v", err)
	}
	err := runDDL(t, ctx, "ALTER PUBLICATION p OWNER TO nosuchrole")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("ExecError.Code = %q, want \"42704\"", ee.Code)
	}
}

// TestAlterPublicationOwnerToUnknownPublicationErrors: ALTER PUBLICATION on
// a name that was never CREATEd must fail rather than silently no-op.
func TestAlterPublicationOwnerToUnknownPublicationErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	err := runDDL(t, ctx, "ALTER PUBLICATION nosuchpub OWNER TO current_user")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("ExecError.Code = %q, want \"42704\"", ee.Code)
	}
}

// TestAlterSubscriptionOwnerTo is TestAlterPublicationOwnerTo's ALTER
// SUBSCRIPTION counterpart.
func TestAlterSubscriptionOwnerTo(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("new_sub_owner")
	roleOID, ok := im.RoleOID("new_sub_owner")
	if !ok {
		t.Fatal("new_sub_owner not registered")
	}

	sql := "CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (enabled = false)"
	if err := runDDL(t, ctx, sql); err != nil {
		t.Fatalf("runDDL create: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s OWNER TO new_sub_owner"); err != nil {
		t.Fatalf("runDDL alter owner: %v", err)
	}
	sub, ok := ctx.PubSub.LookupSubscription("s")
	if !ok {
		t.Fatal("subscription s not registered")
	}
	if sub.Owner != roleOID {
		t.Errorf("sub.Owner = %d, want %d (new_sub_owner's OID)", sub.Owner, roleOID)
	}
}

// wantExecErr asserts err is an *ExecError with the given code and a
// message containing substr.
func wantExecErr(t *testing.T, err error, code, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s %q, got nil", code, substr)
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != code {
		t.Errorf("ExecError.Code = %q, want %q (err=%v)", ee.Code, code, err)
	}
	if !strings.Contains(ee.Message, substr) {
		t.Errorf("message %q does not contain %q", ee.Message, substr)
	}
}

// TestAlterSubscriptionEscapeHatch pins the escape-hatch trio that
// DROP SUBSCRIPTION's 08006 hint names ("ALTER SUBSCRIPTION ... DISABLE" /
// "SET (slot_name = NONE)") and that the 0008-0003 design doc lists as
// CREATE/DROP's residual: ENABLE / DISABLE / SET (slot_name = ...).
// Error arms mirror subscriptioncmds.c's AlterSubscription: 42704 for an
// unknown name, 55000 "cannot enable subscription that does not have a
// slot name" (1344-1348), 55000 "cannot set slot_name = NONE for enabled
// subscription" (1172-1178), 42601 for an option outside ALTER's
// supported set (parse_subscription_options), 22023 for a bad origin.
func TestAlterSubscriptionEscapeHatch(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	create := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("runDDL %q: %v", sql, err)
		}
	}
	// Enabled sub: default slot_name = subscription name "s".
	create("CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1")
	// Disabled sub created with slot_name = NONE (the escape-hatch end
	// state; CREATE requires the companions enabled=false, create_slot=false
	// — subscriptioncmds.c:399-440).
	create("CREATE SUBSCRIPTION gone CONNECTION 'host=remote dbname=app' PUBLICATION p1 WITH (connect = false, enabled = false, create_slot = false, slot_name = NONE)")

	// ENABLE / DISABLE mutate Enabled; the no-slot arm refuses ENABLE.
	// LookupSubscription returns a snapshot copy, so re-look-up after each
	// ALTER.
	subOf := func(name string) *catalog.Subscription {
		s, ok := ctx.PubSub.LookupSubscription(name)
		if !ok {
			t.Fatalf("subscription %s not registered", name)
		}
		return s
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s DISABLE"); err != nil {
		t.Fatalf("DISABLE: %v", err)
	}
	if subOf("s").Enabled {
		t.Error("Enabled=true after DISABLE")
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s ENABLE"); err != nil {
		t.Fatalf("ENABLE: %v", err)
	}
	if !subOf("s").Enabled {
		t.Error("Enabled=false after ENABLE")
	}
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION gone ENABLE"),
		"55000", "cannot enable subscription that does not have a slot name")

	// SET (slot_name = ...): rename writes straight through (PG's namein
	// write, no slot-name validation on this path); NONE requires the
	// sub disabled first — DISABLE then NONE runs (the documented
	// sequence).
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (slot_name = 's_new')"); err != nil {
		t.Fatalf("SET slot_name: %v", err)
	}
	if subOf("s").SlotName != "s_new" {
		t.Errorf("SlotName = %q, want s_new", subOf("s").SlotName)
	}
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (slot_name = NONE)"),
		"55000", "cannot set slot_name = NONE for enabled subscription")
	if subOf("s").SlotName != "s_new" {
		t.Errorf("SlotName = %q after refused NONE, want s_new untouched", subOf("s").SlotName)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s DISABLE"); err != nil {
		t.Fatalf("DISABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (slot_name = NONE)"); err != nil {
		t.Fatalf("DISABLED SET slot_name = NONE: %v", err)
	}
	if subOf("s").SlotName != "" {
		t.Errorf("SlotName = %q, want \"\" (NONE)", subOf("s").SlotName)
	}

	// Option validation arms: ALTER's supported set rejects CREATE-only
	// options as unrecognized (PG's per-call supported_opts), values run
	// through CREATE's defGet* twins, and an empty list is a no-op.
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (connect = false)"),
		"42601", "unrecognized subscription parameter")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (enabled = true)"),
		"42601", "unrecognized subscription parameter")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (foo = 'x')"),
		"42601", "unrecognized subscription parameter: \"foo\"")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (binary = 'nope')"),
		"42601", "binary requires a Boolean value")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (streaming = 'nope')"),
		"42601", "streaming requires a Boolean value")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (origin = 'nope')"),
		"22023", "unrecognized origin value")
	// Accepted-not-modelled options validate and succeed; an empty list
	// changes nothing.
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s SET (synchronous_commit = 'remote_apply', binary = true, two_phase = false, origin = any)"); err != nil {
		t.Fatalf("accepted-not-modelled SET: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s SET ()"); err != nil {
		t.Fatalf("empty SET list: %v", err)
	}
	if s2 := subOf("s"); s2.SlotName != "" || s2.Enabled {
		t.Errorf("state after SET (): SlotName=%q Enabled=%v, want \"\"/false preserved by the no-op (sub was disabled above)", s2.SlotName, s2.Enabled)
	}

	// Unknown subscription name is 42704 on both arms.
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION nosuch ENABLE"), "42704", `subscription "nosuch" does not exist`)
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION nosuch SET (slot_name = 'x')"), "42704", `subscription "nosuch" does not exist`)
}

// TestCheckCmdReplicaIdentity pins CheckCmdReplicaIdentity
// (execReplication.c): UPDATE / DELETE on a table that publishes them but has
// no replica identity is refused with 55000; a primary key, REPLICA IDENTITY
// FULL, or a publication that does not publish the operation lets it run; a
// DEFERRABLE primary key is not a replica identity (RelationGetIndexList).
func TestCheckCmdReplicaIdentity(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	im := ctx.Catalog.(*catalog.InMemory)
	mk := func(name string) *catalog.Table {
		tbl, err := im.CreateTable(parser.ObjectName{Schema: "public", Name: name}, []catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
			{Name: "v", Type: catalog.Type{Name: "text"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return tbl
	}
	nokey, withpk, defpk, full, insonly := mk("nokey"), mk("withpk"), mk("defpk"), mk("full"), mk("insonly")
	if _, err := im.CreateIndex(parser.ObjectName{Schema: "public", Name: "withpk_pkey"}, withpk, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	dpk, err := im.CreateIndex(parser.ObjectName{Schema: "public", Name: "defpk_pkey"}, defpk, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	dpk.Deferrable = true
	full.ReplicaIdentity = "f"
	for _, q := range []string{
		"CREATE PUBLICATION p FOR TABLE nokey, withpk, defpk, full",
		"CREATE PUBLICATION pi FOR TABLE insonly WITH (publish = 'insert')",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cases := []struct {
		tbl      *catalog.Table
		isUpdate bool
		wantErr  string
	}{
		{nokey, true, `cannot update table "nokey" because it does not have a replica identity and publishes updates`},
		{nokey, false, `cannot delete from table "nokey" because it does not have a replica identity and publishes deletes`},
		{defpk, true, `cannot update table "defpk" because it does not have a replica identity and publishes updates`},
		{withpk, true, ""},
		{full, false, ""},
		{insonly, true, ""},
	}
	for _, c := range cases {
		err := checkCmdReplicaIdentity(ctx, c.tbl, c.isUpdate)
		var ee *ExecError
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s update=%v: unexpected %v", c.tbl.Name, c.isUpdate, err)
		case c.wantErr != "" && (!errors.As(err, &ee) || ee.Code != "55000" || ee.Message != c.wantErr):
			t.Errorf("%s update=%v: got %v, want 55000 %q", c.tbl.Name, c.isUpdate, err, c.wantErr)
		}
	}
}

// TestAlterPublicationTables pins AlterPublicationTables (publicationcmds.c):
// ADD appends (refusing a member, 42710), DROP removes (refusing a
// non-member, 42704), SET replaces with duplicates folded (OpenTableList),
// and a FOR ALL TABLES publication refuses all three (55000). It used to be a
// silent no-op, so pg_dump's `ALTER PUBLICATION p ADD TABLE ONLY s.t` restored
// no membership.
func TestAlterPublicationTables(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	im := ctx.Catalog.(*catalog.InMemory)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := im.CreateTable(parser.ObjectName{Schema: "public", Name: n}, []catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustDDL := func(q string) {
		t.Helper()
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	wantErr := func(q, code, msg string) {
		t.Helper()
		var ee *ExecError
		if err := runDDL(t, ctx, q); !errors.As(err, &ee) || ee.Code != code || ee.Message != msg {
			t.Errorf("%s: got %v, want %s %q", q, err, code, msg)
		}
	}
	tables := func() []string {
		pub, _ := ctx.PubSub.LookupPublication("p")
		return pub.Tables
	}
	mustDDL("CREATE PUBLICATION p FOR TABLE a")
	mustDDL("ALTER PUBLICATION p ADD TABLE ONLY public.b, c")
	if got := tables(); len(got) != 3 || got[0] != "public.a" || got[1] != "public.b" || got[2] != "public.c" {
		t.Fatalf("after ADD: %v", got)
	}
	wantErr("ALTER PUBLICATION p ADD TABLE a", "42710", `relation "a" is already member of publication "p"`)
	mustDDL("ALTER PUBLICATION p DROP TABLE a")
	wantErr("ALTER PUBLICATION p DROP TABLE a", "42704", `relation "a" is not part of the publication`)
	mustDDL("ALTER PUBLICATION p SET TABLE c, c")
	if got := tables(); len(got) != 1 || got[0] != "public.c" {
		t.Fatalf("after SET: %v", got)
	}
	mustDDL("CREATE PUBLICATION pall FOR ALL TABLES")
	wantErr("ALTER PUBLICATION pall ADD TABLE a", "55000", `publication "pall" is defined as FOR ALL TABLES`)
	wantErr("ALTER PUBLICATION nopub ADD TABLE a", "42704", `publication "nopub" does not exist`)
}

// TestPublicationDDLForms pins three publication DDL forms against
// publicationcmds.c:
//   - ALTER PUBLICATION ... SET ( publish = ... ) re-parses the actions
//     (AlterPublicationOptions); a bad value or parameter is 42601;
//   - DROP PUBLICATION a, b drops all of them; a missing name fails the whole
//     statement before any drop (PG's DROP is transactional), and IF EXISTS
//     skips it;
//   - CREATE PUBLICATION ... FOR TABLE ONLY t accepts the ONLY item.
func TestPublicationDDLForms(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	im := ctx.Catalog.(*catalog.InMemory)
	if _, err := im.CreateTable(parser.ObjectName{Schema: "public", Name: "a"}, []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}}}); err != nil {
		t.Fatal(err)
	}
	mustDDL := func(q string) {
		t.Helper()
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	wantErr := func(q, code, msg string) {
		t.Helper()
		var ee *ExecError
		if err := runDDL(t, ctx, q); !errors.As(err, &ee) || ee.Code != code || ee.Message != msg {
			t.Errorf("%s: got %v, want %s %q", q, err, code, msg)
		}
	}
	exists := func(name string) bool { _, ok := ctx.PubSub.LookupPublication(name); return ok }

	mustDDL("CREATE PUBLICATION p1 FOR TABLE ONLY a")
	mustDDL("ALTER PUBLICATION p1 SET (publish = 'insert')")
	if pub, _ := ctx.PubSub.LookupPublication("p1"); !pub.PublishInsert || pub.PublishUpdate || pub.PublishDelete {
		t.Errorf("after SET publish=insert: %+v", pub)
	}
	wantErr("ALTER PUBLICATION p1 SET (publish = 'cluster')", "42601", `unrecognized value for publication option "publish": "cluster"`)
	wantErr("ALTER PUBLICATION p1 SET (foo = 'x')", "42601", `unrecognized publication parameter: "foo"`)

	mustDDL("CREATE PUBLICATION p2")
	mustDDL("CREATE PUBLICATION p3")
	wantErr("DROP PUBLICATION p2, nosuch", "42704", `publication "nosuch" does not exist`)
	if !exists("p2") {
		t.Fatal("p2 was dropped although the statement failed on a later name")
	}
	mustDDL("DROP PUBLICATION IF EXISTS nosuch, p2")
	mustDDL("DROP PUBLICATION p1, p3")
	if exists("p1") || exists("p2") || exists("p3") {
		t.Error("DROP PUBLICATION left a named publication behind")
	}
}

// TestSubscriptionPublisherSlotHooks pins CREATE / DROP SUBSCRIPTION's
// publisher side (subscriptioncmds.c): the slot is created on the publisher
// only with connect and create_slot (both default true), a failure leaves no
// subscription behind; DROP SUBSCRIPTION drops the slot after removing the
// subscription and restores it when the publisher side fails (PG's DROP
// rolls back); IF EXISTS on a missing subscription is a NOTICE.
func TestSubscriptionPublisherSlotHooks(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	var created, dropped []string
	var failCreate, failDrop error
	ctx.CreateSubscriptionSlot = func(sub, conninfo, slot string) error {
		if failCreate != nil {
			return failCreate
		}
		created = append(created, slot)
		return nil
	}
	ctx.DropSubscriptionSlot = func(sub, conninfo, slot string) error {
		if failDrop != nil {
			return failDrop
		}
		dropped = append(dropped, slot)
		return nil
	}
	const conn = "host=127.0.0.1 port=1 dbname=postgres"
	mustDDL := func(q string) {
		t.Helper()
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exists := func(name string) bool { _, ok := ctx.PubSub.LookupSubscription(name); return ok }

	mustDDL("CREATE SUBSCRIPTION s1 CONNECTION '" + conn + "' PUBLICATION p")
	mustDDL("CREATE SUBSCRIPTION s2 CONNECTION '" + conn + "' PUBLICATION p WITH (create_slot = false, slot_name = 'other')")
	mustDDL("CREATE SUBSCRIPTION s3 CONNECTION '" + conn + "' PUBLICATION p WITH (connect = false, enabled = false, create_slot = false, slot_name = NONE)")
	if fmt.Sprint(created) != "[s1]" {
		t.Errorf("publisher slots created = %v, want only s1's", created)
	}
	failCreate = &ExecError{Code: "08006", Message: `subscription "s4" could not connect to the publisher: refused`}
	if err := runDDL(t, ctx, "CREATE SUBSCRIPTION s4 CONNECTION '"+conn+"' PUBLICATION p"); err == nil || exists("s4") {
		t.Errorf("failed slot creation: err=%v, subscription exists=%v; want an error and no subscription", err, exists("s4"))
	}
	failDrop = &ExecError{Code: "08006", Message: `could not drop replication slot "s1" on publisher: down`}
	if err := runDDL(t, ctx, "DROP SUBSCRIPTION s1"); err == nil || !exists("s1") {
		t.Errorf("failed slot drop: err=%v, subscription exists=%v; want an error and the subscription kept", err, exists("s1"))
	}
	failDrop = nil
	mustDDL("DROP SUBSCRIPTION s1")
	if exists("s1") || fmt.Sprint(dropped) != "[s1]" {
		t.Errorf("after DROP: exists=%v dropped=%v, want gone and [s1]", exists("s1"), dropped)
	}
	ctx.TakeNotices()
	mustDDL("DROP SUBSCRIPTION IF EXISTS nosuch")
	if n := ctx.TakeNotices(); len(n) != 1 || n[0] != `subscription "nosuch" does not exist, skipping` {
		t.Errorf("IF EXISTS notices = %v", n)
	}
}

// TestAlterSubscriptionPublications pins the ALTER SUBSCRIPTION
// { SET | ADD | DROP } PUBLICATION arms (subscriptioncmds.c:1374-1475,
// merge_publications :2402) — the 2026-10-31 ledgered residual "the rest
// of item (a)". List algebra, error arms, the refresh gates (disabled
// 55000 + per-action hint, explicit transaction block 25001), option
// validation (refresh/copy_data only) and the registry state are all
// pinned mutation-style: every arm re-looks-up the registry snapshot.
func TestAlterSubscriptionPublications(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	create := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("runDDL %q: %v", sql, err)
		}
	}
	create("CREATE SUBSCRIPTION s CONNECTION 'host=remote dbname=app' PUBLICATION p1")
	create("CREATE SUBSCRIPTION gone CONNECTION 'host=remote dbname=app' PUBLICATION pg WITH (connect = false, enabled = false, create_slot = false, slot_name = NONE)")

	subs := func(name string) []string {
		t.Helper()
		s, ok := ctx.PubSub.LookupSubscription(name)
		if !ok {
			t.Fatalf("subscription %s not registered", name)
		}
		return s.Publications
	}
	wantPubs := func(name string, want ...string) {
		t.Helper()
		got := subs(name)
		if len(got) != len(want) {
			t.Fatalf("%s Publications = %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s Publications = %v, want %v", name, got, want)
			}
		}
	}

	// SET replaces the list; ADD appends; DROP removes, order-preserving.
	create("ALTER SUBSCRIPTION s SET PUBLICATION p2")
	wantPubs("s", "p2")
	create("ALTER SUBSCRIPTION s ADD PUBLICATION p3, p4")
	wantPubs("s", "p2", "p3", "p4")
	create("ALTER SUBSCRIPTION s DROP PUBLICATION p3")
	wantPubs("s", "p2", "p4")

	// merge_publications refusals: ADD of a present name (42710), DROP of
	// an absent name (42704), drop-to-empty (42704), duplicate in the
	// new list (42710, check_duplicates_in_publist).
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s ADD PUBLICATION p2"),
		"42710", `publication "p2" is already in subscription "s"`)
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s DROP PUBLICATION p9"),
		"42704", `publication "p9" is not in subscription "s"`)
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s DROP PUBLICATION p2, p4"),
		"42704", "cannot drop all the publications from a subscription")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET PUBLICATION a, b, a"),
		"42710", `publication name "a" used more than once`)
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s ADD PUBLICATION q, q"),
		"42710", `publication name "q" used more than once`)
	wantPubs("s", "p2", "p4") // no refused statement touched the list

	// The refresh gates on the disabled subscription: 55000 with the
	// per-action hint (subscriptioncmds.c:1392-1399/1440-1452), and the
	// WITH (refresh = false) escape hatch succeeding.
	checkHint := func(sql, code, msg, hintSub string) {
		t.Helper()
		err := runDDL(t, ctx, sql)
		var ee *ExecError
		if !errors.As(err, &ee) {
			t.Fatalf("%q: err %v, want %s", sql, err, code)
		}
		if ee.Code != code || !strings.Contains(ee.Message, msg) {
			t.Fatalf("%q: %s %q, want %s %q", sql, ee.Code, ee.Message, code, msg)
		}
		if !strings.Contains(ee.Hint, hintSub) {
			t.Errorf("%q: hint %q does not contain %q", sql, ee.Hint, hintSub)
		}
	}
	checkHint("ALTER SUBSCRIPTION gone SET PUBLICATION p1", "55000",
		"ALTER SUBSCRIPTION with refresh is not allowed for disabled subscriptions",
		"Use ALTER SUBSCRIPTION ... SET PUBLICATION ... WITH (refresh = false).")
	checkHint("ALTER SUBSCRIPTION gone ADD PUBLICATION p1", "55000",
		"ALTER SUBSCRIPTION with refresh is not allowed for disabled subscriptions",
		"Use ALTER SUBSCRIPTION ... ADD PUBLICATION ... WITH (refresh = false) instead.")
	// Widen the list so the DROP arm reaches the refresh gate (PG runs
	// merge_publications first — a drop-to-empty is the 42704 above, not
	// the disabled-55000).
	create("ALTER SUBSCRIPTION gone SET PUBLICATION pg, p1 WITH (refresh = false)")
	checkHint("ALTER SUBSCRIPTION gone DROP PUBLICATION p1", "55000",
		"ALTER SUBSCRIPTION with refresh is not allowed for disabled subscriptions",
		"Use ALTER SUBSCRIPTION ... DROP PUBLICATION ... WITH (refresh = false) instead.")
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION gone SET PUBLICATION p1 WITH (refresh = false)"); err != nil {
		t.Fatalf("disabled SET with refresh=false: %v", err)
	}
	wantPubs("gone", "p1")
	// PG ordering pin: on the disabled subscription the drop-to-empty
	// merge refusal still fires first (merge_publications precedes the
	// refresh gates).
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION gone DROP PUBLICATION p1"),
		"42704", "cannot drop all the publications from a subscription")

	// Option arms: the publication arms' supported_opts are refresh +
	// copy_data; CREATE/SET-only keys and typos are 42601 (the SET arm's
	// same shape); values run through defGetSubscriptionBoolean.
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s ADD PUBLICATION p5 WITH (slot_name = 'x')"),
		"42601", "unrecognized subscription parameter: \"slot_name\"")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET PUBLICATION p6 WITH (foo = 'x')"),
		"42601", "unrecognized subscription parameter: \"foo\"")
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET PUBLICATION p6 WITH (refresh = 'nope')"),
		"42601", "refresh requires a Boolean value")
	// copy_data validates and is accepted-not-modelled.
	create("ALTER SUBSCRIPTION s ADD PUBLICATION p5 WITH (copy_data = false, refresh = true)")
	wantPubs("s", "p2", "p4", "p5")

	// PreventInTransactionBlock (refresh default true): inside an explicit
	// block the statement is 25001 and touches nothing; after ROLLBACK the
	// same statement succeeds.
	ctx.Session = NewBasicSession()
	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	wantExecErr(t, runDDL(t, ctx, "ALTER SUBSCRIPTION s SET PUBLICATION solo"),
		"25001", "ALTER SUBSCRIPTION with refresh cannot run inside a transaction block")
	if err := runTransactionStmt(t, ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	wantPubs("s", "p2", "p4", "p5")
	if err := runDDL(t, ctx, "ALTER SUBSCRIPTION s SET PUBLICATION solo"); err != nil {
		t.Fatalf("post-ROLLBACK SET PUBLICATION: %v", err)
	}
	wantPubs("s", "solo")

	// Unknown subscription name is 42704 on all three arms.
	for _, sql := range []string{
		"ALTER SUBSCRIPTION nosuch SET PUBLICATION p",
		"ALTER SUBSCRIPTION nosuch ADD PUBLICATION p",
		"ALTER SUBSCRIPTION nosuch DROP PUBLICATION p",
	} {
		wantExecErr(t, runDDL(t, ctx, sql), "42704", `subscription "nosuch" does not exist`)
	}
}

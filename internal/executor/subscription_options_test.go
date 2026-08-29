package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0134-0174 guards. Before subscription_options.go, CREATE SUBSCRIPTION
// validated nothing at all: every WITH name, every WITH value and every
// connection string was accepted, so a typo'd option or conninfo produced a
// subscription that looked created and could never replicate. Each case below
// pins one upstream check by its exact message and SQLSTATE — the messages are
// the observable contract (subscription.sql compares them byte for byte).

func TestParseSubscriptionOptionsRejects(t *testing.T) {
	for _, c := range []struct {
		name     string
		with     map[string]string
		wantCode string
		wantMsg  string
	}{
		// subscriptioncmds.c:358 — SYNTAX_ERROR, and ALTER-only names are
		// unrecognized on CREATE (refresh/lsn are not in supported_opts).
		{"unknown name", map[string]string{"not_an_option": "3"},
			"42601", `unrecognized subscription parameter: "not_an_option"`},
		{"alter-only refresh", map[string]string{"refresh": "false"},
			"42601", `unrecognized subscription parameter: "refresh"`},
		{"alter-only lsn", map[string]string{"lsn": "0/12345"},
			"42601", `unrecognized subscription parameter: "lsn"`},

		// defGetBoolean / defGetStreamingMode (define.c:94, subscriptioncmds.c).
		{"binary non-boolean", map[string]string{"connect": "false", "binary": "foo"},
			"42601", "binary requires a Boolean value"},
		{"two_phase non-boolean", map[string]string{"connect": "false", "two_phase": "foo"},
			"42601", "two_phase requires a Boolean value"},
		{"disable_on_error non-boolean", map[string]string{"connect": "false", "disable_on_error": "foo"},
			"42601", "disable_on_error requires a Boolean value"},
		{"streaming bad mode", map[string]string{"connect": "false", "streaming": "foo"},
			"42601", `streaming requires a Boolean value or "parallel"`},

		// subscriptioncmds.c:316 — note the DIFFERENT errcode (22023).
		{"origin", map[string]string{"connect": "false", "slot_name": "none", "origin": "foo"},
			"22023", `unrecognized origin value: "foo"`},

		// ReplicationSlotValidateNameInternal (slot.c).
		{"slot name empty", map[string]string{"slot_name": ""},
			"42602", `replication slot name "" is too short`},
		{"slot name bad char", map[string]string{"slot_name": "Bad-Name"},
			"42602", `replication slot name "Bad-Name" contains invalid character`},
		{"slot name too long", map[string]string{"slot_name": strings.Repeat("a", 64)},
			"42622", `replication slot name "` + strings.Repeat("a", 64) + `" is too long`},

		// The connect = false incompatibility set (subscriptioncmds.c:365-393).
		{"connect/copy_data", map[string]string{"connect": "false", "copy_data": "true"},
			"42601", "connect = false and copy_data = true are mutually exclusive options"},
		{"connect/enabled", map[string]string{"connect": "false", "enabled": "true"},
			"42601", "connect = false and enabled = true are mutually exclusive options"},
		{"connect/create_slot", map[string]string{"connect": "false", "create_slot": "true"},
			"42601", "connect = false and create_slot = true are mutually exclusive options"},

		// The slot_name = NONE set (subscriptioncmds.c:404-440). The pairs
		// below are the reason `specified` exists: the SAME clash produces
		// "mutually exclusive" when the user named the second option and
		// "must also set" when it is merely the default.
		{"none/enabled explicit", map[string]string{"slot_name": "none", "enabled": "true"},
			"42601", "slot_name = NONE and enabled = true are mutually exclusive options"},
		{"none/create_slot explicit", map[string]string{"slot_name": "none", "enabled": "false", "create_slot": "true"},
			"42601", "slot_name = NONE and create_slot = true are mutually exclusive options"},
		{"none/enabled default", map[string]string{"slot_name": "none"},
			"42601", "subscription with slot_name = NONE must also set enabled = false"},
		{"none/create_slot default", map[string]string{"slot_name": "none", "enabled": "false"},
			"42601", "subscription with slot_name = NONE must also set create_slot = false"},
		{"none/create_slot=false still needs enabled", map[string]string{"slot_name": "none", "create_slot": "false"},
			"42601", "subscription with slot_name = NONE must also set enabled = false"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseSubscriptionOptions(c.with, 7)
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("error = %v (%T), want *ExecError", err, err)
			}
			if ee.Code != c.wantCode || ee.Message != c.wantMsg {
				t.Errorf("got %s %q, want %s %q", ee.Code, ee.Message, c.wantCode, c.wantMsg)
			}
		})
	}
}

// The accepted combinations, and the default overrides connect = false
// applies (subscriptioncmds.c:390-392) — goopg previously defaulted
// `enabled` to true unconditionally, so `WITH (connect = false)` created an
// ENABLED subscription where PG creates a disabled one.
func TestParseSubscriptionOptionsAccepts(t *testing.T) {
	opts, err := parseSubscriptionOptions(map[string]string{"connect": "false"}, 0)
	if err != nil {
		t.Fatalf("connect = false: %v", err)
	}
	if opts.enabled || opts.createSlot || opts.copyData {
		t.Errorf("connect = false must clear enabled/create_slot/copy_data, got %+v", opts)
	}
	if opts.origin != logicalRepOriginAny || opts.streaming != logicalRepStreamParallel {
		t.Errorf("defaults drifted: origin=%q streaming=%q", opts.origin, opts.streaming)
	}

	opts, err = parseSubscriptionOptions(map[string]string{
		"slot_name": "none", "connect": "false", "origin": "none",
	}, 0)
	if err != nil {
		t.Fatalf("slot_name = NONE + connect = false: %v", err)
	}
	if opts.slotName != "" || !opts.specified["slot_name"] {
		t.Errorf("slot_name = NONE must resolve to no slot, got %q", opts.slotName)
	}
	if opts.origin != logicalRepOriginNone {
		t.Errorf("origin = %q, want none", opts.origin)
	}

	// Every boolean spelling defGetBoolean admits.
	for _, v := range []string{"1", "0", "true", "false", "on", "off", "TRUE", "Off"} {
		if _, err := parseSubscriptionOptions(map[string]string{"connect": "false", "binary": v}, 0); err != nil {
			t.Errorf("binary = %q: %v", v, err)
		}
	}
	for _, v := range []string{"parallel", "PARALLEL", "on", "off", "1", "0"} {
		if _, err := parseSubscriptionOptions(map[string]string{"connect": "false", "streaming": v}, 0); err != nil {
			t.Errorf("streaming = %q: %v", v, err)
		}
	}
}

// checkConninfoSyntax ports PQconninfoParse's keyword=value scan
// (fe-connect.c:6290) plus libpqrcv_check_conninfo's wrapper.
func TestCheckConninfoSyntax(t *testing.T) {
	for _, c := range []struct {
		conninfo string
		wantMsg  string // "" = must be accepted
	}{
		{"dbname=regress_doesnotexist", ""},
		{"dbname=regress_doesnotexist password=regress_fakepassword", ""},
		{"  host = localhost   port = 5432  ", ""},
		{`dbname='quoted value' user=u`, ""},
		{`dbname=with\ escape`, ""},
		{"", ""},
		// URI form is dispatched to conninfo_uri_parse upstream and is
		// deliberately accepted unvalidated here (ledgered).
		{"postgresql://localhost/db", ""},

		{"foo", `invalid connection string syntax: missing "=" after "foo" in connection info string`},
		{"testconn", `invalid connection string syntax: missing "=" after "testconn" in connection info string`},
		{"dbname=d bare", `invalid connection string syntax: missing "=" after "bare" in connection info string`},
		{"i_dont_exist=param", `invalid connection string syntax: invalid connection option "i_dont_exist"`},
		{"dbname=d nosuch=1", `invalid connection string syntax: invalid connection option "nosuch"`},
		{`dbname='unterminated`, "invalid connection string syntax: unterminated quoted string in connection info string"},
	} {
		err := checkConninfoSyntax(c.conninfo, 3)
		if c.wantMsg == "" {
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.conninfo, err)
			}
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%q: error = %v (%T), want *ExecError", c.conninfo, err, err)
			continue
		}
		if ee.Code != "42601" || ee.Message != c.wantMsg {
			t.Errorf("%q: got %s %q, want 42601 %q", c.conninfo, ee.Code, ee.Message, c.wantMsg)
		}
	}
}

// check_duplicates_in_publist names the EARLIER equal name, not the later one
// (subscriptioncmds.c:2362 — the inner loop breaks at the outer cell).
func TestCheckDuplicatesInPublist(t *testing.T) {
	if err := checkDuplicatesInPublist([]string{"foo", "testpub"}, 0); err != nil {
		t.Fatalf("distinct names: %v", err)
	}
	err := checkDuplicatesInPublist([]string{"foo", "testpub", "foo"}, 0)
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error = %v (%T), want *ExecError", err, err)
	}
	if ee.Code != "42710" || ee.Message != `publication name "foo" used more than once` {
		t.Errorf("got %s %q", ee.Code, ee.Message)
	}
}

// The cascade guard. subscription.sql reuses one subscription name across a
// run of negative cases, so a silently-accepted statement creates the
// subscription and every later statement reports a spurious "already exists"
// instead of its own error — 20 of the case's 46 divergences at HEAD. Nothing
// may reach the registry when validation fails.
func TestCreateSubscriptionRejectedEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	for _, c := range []struct {
		stmt    string
		wantMsg string
	}{
		{`CREATE SUBSCRIPTION rej CONNECTION 'testconn' PUBLICATION testpub`,
			`invalid connection string syntax: missing "=" after "testconn" in connection info string`},
		{`CREATE SUBSCRIPTION rej CONNECTION 'i_dont_exist=param' PUBLICATION testpub`,
			`invalid connection string syntax: invalid connection option "i_dont_exist"`},
		{`CREATE SUBSCRIPTION rej CONNECTION 'dbname=d' PUBLICATION foo, testpub, foo WITH (connect = false)`,
			`publication name "foo" used more than once`},
		{`CREATE SUBSCRIPTION rej CONNECTION 'dbname=d' PUBLICATION testpub WITH (connect = false, enabled = true)`,
			`connect = false and enabled = true are mutually exclusive options`},
		{`CREATE SUBSCRIPTION rej CONNECTION 'dbname=d' PUBLICATION testpub WITH (not_an_option = 3)`,
			`unrecognized subscription parameter: "not_an_option"`},
	} {
		err := runDDL(t, ctx, c.stmt)
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("%s: error = %v (%T), want *ExecError", c.stmt, err, err)
		}
		if ee.Message != c.wantMsg {
			t.Errorf("%s: message = %q, want %q", c.stmt, ee.Message, c.wantMsg)
		}
		if _, exists := ctx.PubSub.LookupSubscription("rej", catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); exists {
			t.Fatalf("%s: subscription was created despite the error", c.stmt)
		}
	}

	// A valid statement still works, the duplicate-name message now names the
	// subscription (upstream's `subscription "%s" already exists`, :623), and
	// an unspecified slot_name defaults to the subscription name (:632).
	if err := runDDL(t, ctx, `CREATE SUBSCRIPTION ok_sub CONNECTION 'dbname=d' PUBLICATION testpub WITH (connect = false)`); err != nil {
		t.Fatalf("valid CREATE SUBSCRIPTION: %v", err)
	}
	sub, exists := ctx.PubSub.LookupSubscription("ok_sub", catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !exists {
		t.Fatal("ok_sub was not created")
	}
	if sub.SlotName != "ok_sub" {
		t.Errorf("slot name = %q, want the subscription name", sub.SlotName)
	}
	if sub.Enabled {
		t.Error("connect = false must create a DISABLED subscription")
	}
	err := runDDL(t, ctx, `CREATE SUBSCRIPTION ok_sub CONNECTION 'dbname=d' PUBLICATION testpub WITH (connect = false)`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42710" || ee.Message != `subscription "ok_sub" already exists` {
		t.Errorf("duplicate name: error = %v, want 42710 `subscription \"ok_sub\" already exists`", err)
	}
}

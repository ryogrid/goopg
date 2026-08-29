package executor

import (
	"fmt"
	"sort"
	"strings"
)

// CREATE SUBSCRIPTION option / connection-string validation — M0134-0174.
//
// Until this file existed goopg's CREATE SUBSCRIPTION validated *nothing*.
// execCreateSubscription read exactly two keys out of the WITH map
// (`enabled`, `slot_name`) and dropped every other name on the floor, and
// `s.Conninfo` went straight into the catalog row unread. So all of
//
//	CREATE SUBSCRIPTION s CONNECTION 'foo'          PUBLICATION p;
//	CREATE SUBSCRIPTION s CONNECTION 'i_dont=x'     PUBLICATION p;
//	CREATE SUBSCRIPTION s CONNECTION 'dbname=d'     PUBLICATION p
//	    WITH (connect = false, enabled = true);
//	CREATE SUBSCRIPTION s CONNECTION 'dbname=d'     PUBLICATION p
//	    WITH (not_an_option = 3);
//
// SUCCEEDED, where PG raises (respectively) `invalid connection string
// syntax: missing "=" after "foo" ...`, `invalid connection option
// "i_dont"`, `connect = false and enabled = true are mutually exclusive
// options` and `unrecognized subscription parameter: "not_an_option"`.
//
// That is a silent-acceptance correctness gap in its own right — a typo'd
// conninfo or a misspelt option produces a subscription that looks created
// and can never replicate — and it also cascades exactly the way M0134-0160's
// reloption gap did: subscription.sql reuses one subscription name across a
// run of negative cases, so the first silently-accepted statement creates the
// subscription and all twenty later statements report a spurious
// "subscription already exists" instead of their own error.
//
// Upstream model, all in postgres/src/backend/commands/subscriptioncmds.c:
//
//   - parse_subscription_options (:124) walks the DefElem list against a
//     caller-supplied `supported_opts` bitmask, validating each value with
//     defGetBoolean / defGetStreamingMode / ReplicationSlotValidateName, then
//     applies two post-passes: the `connect = false` incompatibility set
//     (:365) and the `slot_name = NONE` incompatibility set (:404).
//   - CreateSubscription (:539) then runs, in this order: the duplicate-name
//     check (`subscription "%s" already exists`, :623), the slot_name default
//     (:632), walrcv_check_conninfo (:645) and publicationListToArray →
//     check_duplicates_in_publist (:683).
//
// The order is load-bearing and is reproduced verbatim in
// execCreateSubscription: a statement that is bad in two ways must report the
// same one of them PG reports.
//
// See docs/design/m0134-0174-create-subscription-validation.md.

// subOpts mirrors upstream's SubOpts (subscriptioncmds.c:70). `specified`
// stands in for upstream's `specified_opts` bitmask: several of the
// incompatibility rules below distinguish "the user asked for this" from "this
// is merely the default", and produce a *different* message for each.
type subOpts struct {
	connect          bool
	enabled          bool
	createSlot       bool
	copyData         bool
	slotName         string // "" once slot_name = NONE has been resolved
	binary           bool
	streaming        string
	twoPhase         bool
	disableOnErr     bool
	passwordRequired bool
	runAsOwner       bool
	failover         bool
	origin           string
	specified        map[string]bool
}

// createSubscriptionSupportedOpts is upstream's `supported_opts` for
// CreateSubscription (subscriptioncmds.c:560-567). SUBOPT_REFRESH and
// SUBOPT_LSN are deliberately absent — they are ALTER-only, which is why PG
// answers `unrecognized subscription parameter: "refresh"` on CREATE.
var createSubscriptionSupportedOpts = map[string]bool{
	"connect":            true,
	"enabled":            true,
	"create_slot":        true,
	"slot_name":          true,
	"copy_data":          true,
	"synchronous_commit": true,
	"binary":             true,
	"streaming":          true,
	"two_phase":          true,
	"disable_on_error":   true,
	"password_required":  true,
	"run_as_owner":       true,
	"failover":           true,
	"origin":             true,
}

// Upstream's LOGICALREP_ORIGIN_NONE / LOGICALREP_ORIGIN_ANY
// (postgres/src/include/replication/logicalproto.h).
const (
	logicalRepOriginNone = "none"
	logicalRepOriginAny  = "any"
)

// Upstream's LOGICALREP_STREAM_OFF / _ON / _PARALLEL
// (postgres/src/include/catalog/pg_subscription.h).
const (
	logicalRepStreamOff      = "f"
	logicalRepStreamOn       = "t"
	logicalRepStreamParallel = "p"
)

// defGetSubscriptionBoolean ports defGetBoolean
// (postgres/src/backend/commands/define.c:94) for the subscription WITH map.
//
// Upstream accepts the integers 0/1 and the strings true/false/on/off
// case-insensitively, and raises `%s requires a Boolean value` otherwise.
// goopg's WITH clause is a map[string]string, so the T_Integer arm and the
// T_String arm collapse into one: `binary = 0` and `binary = '0'` are
// indistinguishable here, where upstream accepts only the former. Recorded in
// the deferral ledger rather than papered over.
func defGetSubscriptionBoolean(name, val string, pos int) (bool, error) {
	switch strings.ToLower(val) {
	case "1", "true", "on":
		return true, nil
	case "0", "false", "off":
		return false, nil
	}
	return false, &ExecError{
		Code:    "42601",
		Pos:     pos,
		Message: fmt.Sprintf("%s requires a Boolean value", name),
	}
}

// defGetStreamingMode ports the same-named upstream function
// (subscriptioncmds.c). Same 0/1-vs-"0"/"1" collapse as
// defGetSubscriptionBoolean.
func defGetStreamingMode(name, val string, pos int) (string, error) {
	switch strings.ToLower(val) {
	case "0", "false", "off":
		return logicalRepStreamOff, nil
	case "1", "true", "on":
		return logicalRepStreamOn, nil
	case "parallel":
		return logicalRepStreamParallel, nil
	}
	return "", &ExecError{
		Code:    "42601",
		Pos:     pos,
		Message: fmt.Sprintf("%s requires a Boolean value or \"parallel\"", name),
	}
}

// validateReplicationSlotName ports ReplicationSlotValidateNameInternal
// (postgres/src/backend/replication/slot.c). NAMEDATALEN is 64, so the
// too-long bound is >= 64 bytes.
func validateReplicationSlotName(name string, pos int) error {
	if len(name) == 0 {
		return &ExecError{
			Code:    "42602",
			Pos:     pos,
			Message: fmt.Sprintf("replication slot name %q is too short", name),
		}
	}
	if len(name) >= 64 {
		return &ExecError{
			Code:    "42622",
			Pos:     pos,
			Message: fmt.Sprintf("replication slot name %q is too long", name),
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return &ExecError{
			Code:    "42602",
			Pos:     pos,
			Message: fmt.Sprintf("replication slot name %q contains invalid character", name),
			Hint:    "Replication slot names may only contain lower case letters, numbers, and the underscore character.",
		}
	}
	return nil
}

// parseSubscriptionOptions ports parse_subscription_options
// (subscriptioncmds.c:124) for CreateSubscription's supported-option set.
//
// Divergence recorded in the ledger: upstream walks the DefElem list in SOURCE
// order, so with two bad options it reports the textually-first. goopg's WITH
// clause reaches the executor as a map, so the walk is over sorted keys and the
// reported offender is the lexicographically-first. Sorting (rather than Go's
// randomised map order) is what makes the error deterministic at all — the same
// choice M0134-0160 made for reloptions, and the same fix unblocks both
// (an ordered name list on the statement node).
func parseSubscriptionOptions(with map[string]string, pos int) (*subOpts, error) {
	// Upstream's defaults for the supported set (subscriptioncmds.c:142-166).
	opts := &subOpts{
		connect:          true,
		enabled:          true,
		createSlot:       true,
		copyData:         true,
		binary:           false,
		streaming:        logicalRepStreamParallel,
		twoPhase:         false,
		disableOnErr:     false,
		passwordRequired: true,
		runAsOwner:       false,
		failover:         false,
		origin:           logicalRepOriginAny,
		specified:        map[string]bool{},
	}

	names := make([]string, 0, len(with))
	for k := range with {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		val := with[name]
		if !createSubscriptionSupportedOpts[name] {
			// subscriptioncmds.c:358 — ERRCODE_SYNTAX_ERROR, not 22023.
			return nil, &ExecError{
				Code:    "42601",
				Pos:     pos,
				Message: fmt.Sprintf("unrecognized subscription parameter: %q", name),
			}
		}
		opts.specified[name] = true

		switch name {
		case "connect", "enabled", "create_slot", "copy_data",
			"binary", "two_phase", "disable_on_error",
			"password_required", "run_as_owner", "failover":
			b, err := defGetSubscriptionBoolean(name, val, pos)
			if err != nil {
				return nil, err
			}
			switch name {
			case "connect":
				opts.connect = b
			case "enabled":
				opts.enabled = b
			case "create_slot":
				opts.createSlot = b
			case "copy_data":
				opts.copyData = b
			case "binary":
				opts.binary = b
			case "two_phase":
				opts.twoPhase = b
			case "disable_on_error":
				opts.disableOnErr = b
			case "password_required":
				opts.passwordRequired = b
			case "run_as_owner":
				opts.runAsOwner = b
			case "failover":
				opts.failover = b
			}
		case "slot_name":
			// subscriptioncmds.c:205-213: `slot_name = NONE` is spelled as the
			// literal string "none" by the time it reaches here, and means
			// "no slot"; anything else must be a legal slot name.
			if val == "none" {
				opts.slotName = ""
			} else {
				if err := validateReplicationSlotName(val, pos); err != nil {
					return nil, err
				}
				opts.slotName = val
			}
		case "streaming":
			s, err := defGetStreamingMode(name, val, pos)
			if err != nil {
				return nil, err
			}
			opts.streaming = s
		case "origin":
			// subscriptioncmds.c:316-322. Note the errcode differs from every
			// other check in this function: INVALID_PARAMETER_VALUE, not
			// SYNTAX_ERROR.
			if !strings.EqualFold(val, logicalRepOriginNone) &&
				!strings.EqualFold(val, logicalRepOriginAny) {
				return nil, &ExecError{
					Code:    "22023",
					Pos:     pos,
					Message: fmt.Sprintf("unrecognized origin value: %q", val),
				}
			}
			opts.origin = strings.ToLower(val)
		case "synchronous_commit":
			// Upstream validates this by running it through set_config_option
			// in PGC_S_TEST mode (subscriptioncmds.c:229-231). goopg's GUC
			// registry is not reachable from the DDL operator, so the value is
			// accepted unvalidated here — ledgered, not silently intended.
		}
	}

	// "We've been explicitly asked to not connect" (subscriptioncmds.c:363-393).
	// The `specified` guard is what separates "you asked for both" (an error)
	// from "the default clashes" (silently overridden below).
	if !opts.connect {
		if opts.enabled && opts.specified["enabled"] {
			return nil, mutuallyExclusiveSubOpts("connect = false", "enabled = true", pos)
		}
		if opts.createSlot && opts.specified["create_slot"] {
			return nil, mutuallyExclusiveSubOpts("connect = false", "create_slot = true", pos)
		}
		if opts.copyData && opts.specified["copy_data"] {
			return nil, mutuallyExclusiveSubOpts("connect = false", "copy_data = true", pos)
		}
		opts.enabled = false
		opts.createSlot = false
		opts.copyData = false
	}

	// "disallowed combination when slot_name = NONE was used"
	// (subscriptioncmds.c:399-440). Reached only when slot_name was given AND
	// resolved to no slot, so `specified` is checked on the *other* option to
	// pick between the two message shapes.
	if opts.slotName == "" && opts.specified["slot_name"] {
		if opts.enabled {
			if opts.specified["enabled"] {
				return nil, mutuallyExclusiveSubOpts("slot_name = NONE", "enabled = true", pos)
			}
			return nil, mustAlsoSetSubOpt("slot_name = NONE", "enabled = false", pos)
		}
		if opts.createSlot {
			if opts.specified["create_slot"] {
				return nil, mutuallyExclusiveSubOpts("slot_name = NONE", "create_slot = true", pos)
			}
			return nil, mustAlsoSetSubOpt("slot_name = NONE", "create_slot = false", pos)
		}
	}

	return opts, nil
}

func mutuallyExclusiveSubOpts(a, b string, pos int) error {
	return &ExecError{
		Code:    "42601",
		Pos:     pos,
		Message: fmt.Sprintf("%s and %s are mutually exclusive options", a, b),
	}
}

func mustAlsoSetSubOpt(a, b string, pos int) error {
	return &ExecError{
		Code:    "42601",
		Pos:     pos,
		Message: fmt.Sprintf("subscription with %s must also set %s", a, b),
	}
}

// checkDuplicatesInPublist ports check_duplicates_in_publist
// (subscriptioncmds.c:2362). Upstream reports the FIRST name that has an
// earlier equal — the inner loop breaks at the outer cell — so
// `PUBLICATION foo, testpub, foo` names "foo".
func checkDuplicatesInPublist(publist []string, pos int) error {
	for i, name := range publist {
		for j := 0; j < i; j++ {
			if publist[j] == name {
				return &ExecError{
					Code:    "42710",
					Pos:     pos,
					Message: fmt.Sprintf("publication name %q used more than once", publist[j]),
				}
			}
		}
	}
	return nil
}

// URI designators recognised by uri_prefix_length
// (postgres/src/interfaces/libpq/fe-connect.c).
var conninfoURIPrefixes = []string{"postgresql://", "postgres://"}

// checkConninfoSyntax ports walrcv_check_conninfo's syntax half
// (libpqrcv_check_conninfo, postgres/src/backend/replication/
// libpqwalreceiver/libpqwalreceiver.c) — that is, PQconninfoParse plus the
// `invalid connection string syntax: %s` wrapper, at ERRCODE_SYNTAX_ERROR.
//
// The keyword=value scan below is conninfo_parse (fe-connect.c:6290)
// transcribed: names run to the first '=' or whitespace, a missing '=' is
// fatal, values are either bare (with backslash escapes) or single-quoted, and
// an unknown name is rejected by conninfo_storeval (:6530).
//
// The URI form is dispatched separately upstream (parse_connection_string →
// conninfo_uri_parse) and is accepted unvalidated here — ledgered.
//
// The `must_use_password` half of libpqrcv_check_conninfo is NOT ported: it
// depends on the superuser/permission checks that CREATE SUBSCRIPTION does not
// yet run at all. Also ledgered.
func checkConninfoSyntax(conninfo string, pos int) error {
	for _, p := range conninfoURIPrefixes {
		if strings.HasPrefix(conninfo, p) {
			return nil
		}
	}
	invalid := func(msg string) error {
		return &ExecError{
			Code:    "42601",
			Pos:     pos,
			Message: "invalid connection string syntax: " + msg,
		}
	}

	s := conninfo
	i := 0
	for i < len(s) {
		if isConninfoSpace(s[i]) {
			i++
			continue
		}
		// Parameter name: up to '=' or whitespace.
		nameStart := i
		for i < len(s) && s[i] != '=' && !isConninfoSpace(s[i]) {
			i++
		}
		name := s[nameStart:i]
		// Skip whitespace between the name and the '='.
		for i < len(s) && isConninfoSpace(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return invalid(fmt.Sprintf("missing %q after %q in connection info string", "=", name))
		}
		i++ // consume '='
		for i < len(s) && isConninfoSpace(s[i]) {
			i++
		}
		// Parameter value: quoted or bare.
		if i < len(s) && s[i] == '\'' {
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' {
					i++
					if i < len(s) {
						i++
					}
					continue
				}
				if s[i] == '\'' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return invalid("unterminated quoted string in connection info string")
			}
		} else {
			for i < len(s) && !isConninfoSpace(s[i]) {
				if s[i] == '\\' {
					i++
					if i < len(s) {
						i++
					}
					continue
				}
				i++
			}
		}
		if !libpqConninfoKeywords[name] {
			return invalid(fmt.Sprintf("invalid connection option %q", name))
		}
	}
	return nil
}

// isConninfoSpace mirrors the isspace() calls in conninfo_parse.
func isConninfoSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// libpqConninfoKeywords is the keyword column of PQconninfoOptions
// (postgres/src/interfaces/libpq/fe-connect.c). conninfo_storeval rejects any
// name not in this table with `invalid connection option "%s"`.
var libpqConninfoKeywords = map[string]bool{
	"service": true, "user": true, "password": true, "passfile": true,
	"channel_binding": true, "connect_timeout": true, "dbname": true,
	"host": true, "hostaddr": true, "port": true, "client_encoding": true,
	"options": true, "application_name": true, "fallback_application_name": true,
	"keepalives": true, "keepalives_idle": true, "keepalives_interval": true,
	"keepalives_count": true, "tcp_user_timeout": true, "sslmode": true,
	"sslnegotiation": true, "sslcompression": true, "sslcert": true,
	"sslkey": true, "sslcertmode": true, "sslpassword": true,
	"sslrootcert": true, "sslcrl": true, "sslcrldir": true, "sslsni": true,
	"requirepeer": true, "require_auth": true, "min_protocol_version": true,
	"max_protocol_version": true, "ssl_min_protocol_version": true,
	"ssl_max_protocol_version": true, "gssencmode": true, "krbsrvname": true,
	"gsslib": true, "gssdelegation": true, "replication": true,
	"target_session_attrs": true, "load_balance_hosts": true,
	"scram_client_key": true, "scram_server_key": true, "oauth_issuer": true,
	"oauth_client_id": true, "oauth_client_secret": true, "oauth_scope": true,
	"sslkeylogfile": true,
}

package executor

import "testing"

// TestFormatTypeOIDInternalPseudoType pins formatTypeOID(2281, ...) →
// "internal", the pseudo-type used by fmgr-internal-only functions (trigger
// bodies, index AM handlers, and — the case this closes — the sole arg/
// rettype PG's check_transform_function requires of a CREATE TRANSFORM WITH
// FUNCTION clause). Before this, OID 2281 fell through to the default "???"
// fallback (goopg's own mirror of PG's format_type.c unresolved-OID string),
// so pg_dump's getFormattedTypeName — which calls
// `format_type('2281'::oid, NULL)` server-side for any OID not already in its
// client-side TypeInfo cache — rendered `WITH FUNCTION pg_catalog.int4recv(???)`
// instead of `...(internal)`. DU-002 slice 404 connsetup fixture
// (TestPort_PgDumpConnectionSetup) exercises this same path end-to-end.
func TestFormatTypeOIDInternalPseudoType(t *testing.T) {
	if got := formatTypeOID(2281, -1); got != "internal" {
		t.Errorf("formatTypeOID(2281, -1) = %q, want %q", got, "internal")
	}
}

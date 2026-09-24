package misc

import (
	"strings"
	"testing"
)

// TestSSLGUCFamilyNoSSLBuild pins the ssl* GUCs to guc_tables.c's values for
// a build without USE_SSL — the PG 18.3 oracle's configuration, measured on it
// (docs/design/0100-0149/0122-0008-ssl-guc-family-nossl-build.md). Before this
// family was registered, SHOW ssl and any postgresql.conf naming an SSL
// setting failed with "unrecognized configuration parameter".
func TestSSLGUCFamilyNoSSLBuild(t *testing.T) {
	cases := []struct {
		name, boot string
		typ        Type
		ctx        Context
	}{
		{"ssl", "off", TypeBool, ContextSigHup},
		{"ssl_ca_file", "", TypeString, ContextSigHup},
		{"ssl_cert_file", "server.crt", TypeString, ContextSigHup},
		{"ssl_ciphers", "none", TypeString, ContextSigHup},
		{"ssl_crl_dir", "", TypeString, ContextSigHup},
		{"ssl_crl_file", "", TypeString, ContextSigHup},
		{"ssl_dh_params_file", "", TypeString, ContextSigHup},
		{"ssl_groups", "none", TypeString, ContextSigHup},
		{"ssl_key_file", "server.key", TypeString, ContextSigHup},
		{"ssl_library", "", TypeString, ContextInternal},
		{"ssl_max_protocol_version", "", TypeEnum, ContextSigHup},
		{"ssl_min_protocol_version", "TLSv1.2", TypeEnum, ContextSigHup},
		{"ssl_passphrase_command", "", TypeString, ContextSigHup},
		{"ssl_passphrase_command_supports_reload", "off", TypeBool, ContextSigHup},
		{"ssl_prefer_server_ciphers", "on", TypeBool, ContextSigHup},
		{"ssl_renegotiation_limit", "0", TypeInt, ContextUserset},
		{"ssl_tls13_ciphers", "", TypeString, ContextSigHup},
	}
	s := NewSessionRegistry(BuildDefaultRegistry())
	for _, tc := range cases {
		v, val, ok := s.Get(tc.name)
		if !ok {
			t.Errorf("%s not registered", tc.name)
			continue
		}
		if val != tc.boot || v.Type != tc.typ || v.Context != tc.ctx {
			t.Errorf("%s = (%q, type %v, context %v), want (%q, %v, %v)",
				tc.name, val, v.Type, v.Context, tc.boot, tc.typ, tc.ctx)
		}
	}
}

// TestSSLOnRejected is check_ssl without USE_SSL. The check sees the
// canonical value, so every spelling of true is refused, not just "on".
func TestSSLOnRejected(t *testing.T) {
	v, ok := BuildDefaultRegistry().Get("ssl")
	if !ok {
		t.Fatal("ssl not registered")
	}
	for _, spelling := range []string{"on", "true", "yes", "1"} {
		_, err := v.canonicalize(spelling)
		if err == nil || err.Error() != "SSL is not supported by this build" {
			t.Errorf("ssl = %s: err = %v, want \"SSL is not supported by this build\"", spelling, err)
		}
	}
	if got, err := v.canonicalize("false"); err != nil || got != "off" {
		t.Errorf("ssl = false: (%q, %v), want (off, nil)", got, err)
	}
}

// TestSSLOnInConfigFileFailsStartup: the oracle refuses to start ("SSL is not
// supported by this build", then FATAL "configuration file ... contains
// errors") and a reload keeps ssl off. Both goopg paths validate through the
// check hook.
func TestSSLOnInConfigFileFailsStartup(t *testing.T) {
	entries := []ConfigEntry{{Name: "ssl", Value: "on", SourceFile: "postgresql.conf", SourceLine: 7}}
	err := BuildDefaultRegistry().ApplyConfigEntries(entries)
	if err == nil || !strings.Contains(err.Error(), "SSL is not supported by this build") {
		t.Errorf("boot load: err = %v, want the check_ssl message", err)
	}
	r := BuildDefaultRegistry()
	res := r.ApplyReloadEntries(entries)
	if len(res.Changed) != 0 || len(res.Warnings) != 1 {
		t.Errorf("reload: changed=%v warnings=%v, want no change and one warning", res.Changed, res.Warnings)
	}
	if v, _ := r.Get("ssl"); v.Value != "off" {
		t.Errorf("reload left ssl = %q, want off", v.Value)
	}
}

// TestSSLRenegotiationLimitRange: range 0 .. 0, which goopg's MinVal/MaxVal
// cannot express (0/0 means unbounded), so a CheckFn carries it.
func TestSSLRenegotiationLimitRange(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	err := s.Set("ssl_renegotiation_limit", "5", false)
	want := `5 is outside the valid range for parameter "ssl_renegotiation_limit" (0 .. 0)`
	if err == nil || err.Error() != want {
		t.Errorf("SET 5: err = %v, want %q", err, want)
	}
	if err := s.Set("ssl_renegotiation_limit", "0", false); err != nil {
		t.Errorf("SET 0: %v", err)
	}
}

// TestShowAllOmitsNoShowAll: SHOW ALL skips GUC_NO_SHOW_ALL variables
// (guc_funcs.c ShowAllGUCConfig) but SHOW by name still works. The PG 18.3
// oracle's SHOW ALL lists none of these four.
func TestShowAllOmitsNoShowAll(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())
	hidden := map[string]bool{
		"is_superuser": true, "session_authorization": true,
		"default_with_oids": true, "ssl_renegotiation_limit": true,
	}
	sslRows := 0
	for _, kv := range s.AllDisplay() {
		if hidden[kv.Name] {
			t.Errorf("SHOW ALL lists %s", kv.Name)
		}
		if strings.HasPrefix(kv.Name, "ssl") {
			sslRows++
		}
	}
	if sslRows != 16 {
		t.Errorf("SHOW ALL lists %d ssl rows, want 16 as on the oracle", sslRows)
	}
	for name := range hidden {
		if _, _, ok := s.Get(name); !ok {
			t.Errorf("SHOW %s: not reachable by name", name)
		}
	}
}

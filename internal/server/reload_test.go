package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/config"
)

// TestReloadConfigAppliesSigHupSkipsPostmaster exercises reloadConfig
// end-to-end through the same public entry points the control-socket
// RELOAD command and SIGHUP use: it re-reads cfg.ConfigPath and
// applies the new values to cfg.Registry. Verifies a PGC_SIGHUP GUC
// (checkpoint_timeout) picks up the new value while a PGC_POSTMASTER
// one (max_connections) is left untouched, matching upstream's
// ProcessConfigFile split.
func TestReloadConfigAppliesSigHupSkipsPostmaster(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "postgresql.conf")
	if err := os.WriteFile(confPath, []byte("checkpoint_timeout = 600\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := config.BuildDefaultRegistry()
	s := New(Config{
		DataDir:    dir,
		Registry:   registry,
		ConfigPath: confPath,
	})

	s.reloadConfig()

	ct, _ := registry.Get("checkpoint_timeout")
	if ct.Value != "600" {
		t.Errorf("checkpoint_timeout = %q, want %q after reload", ct.Value, "600")
	}

	// Rewrite the file to also set a PGC_POSTMASTER GUC and reload
	// again; it must be reported (via the logger) but not applied.
	if err := os.WriteFile(confPath, []byte("checkpoint_timeout = 900\nmax_connections = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeMaxConn, _ := registry.Get("max_connections")
	before := beforeMaxConn.Value

	s.reloadConfig()

	ct, _ = registry.Get("checkpoint_timeout")
	if ct.Value != "900" {
		t.Errorf("checkpoint_timeout = %q, want %q after second reload", ct.Value, "900")
	}
	mc, _ := registry.Get("max_connections")
	if mc.Value != before {
		t.Errorf("max_connections = %q, want unchanged %q (PGC_POSTMASTER must survive reload untouched)", mc.Value, before)
	}
}

// TestReloadConfigNoPathIsNoop verifies a server started without a
// config file (ConfigPath empty) tolerates a RELOAD/SIGHUP without
// panicking or touching the registry.
func TestReloadConfigNoPathIsNoop(t *testing.T) {
	registry := config.BuildDefaultRegistry()
	before := registry.All()
	s := New(Config{Registry: registry})

	s.reloadConfig()

	after := registry.All()
	if len(before) != len(after) {
		t.Fatalf("registry variable count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Value != after[i].Value {
			t.Errorf("%s changed from %q to %q on a no-config-path reload", before[i].Name, before[i].Value, after[i].Value)
		}
	}
}

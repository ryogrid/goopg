package postmaster

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestAppendTypedCellTextByteaHonorsByteaOutput pins the wire text-format
// path — appendTypedCellText's "bytea" case, the SELECT/simple-query output a
// psql client actually reads — against the `bytea_output` GUC. Before this
// fix the case hardcoded `\x` + hex regardless of the GUC (M0097-0035).
// Expectations for the "escape" mode are captured LIVE from PG 18.3
// (postgres/local_install/bin; see the leaf package's
// TestByteaOutEscapeBoundaryBytes for the exact capture transcript).
// M0134-0001 S12.
func TestAppendTypedCellTextByteaHonorsByteaOutput(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	byteaType := catalog.Type{Name: "bytea"}
	d := executor.NewBytesDatum([]byte{0x00, 0x01, 0x1f, 0x20, 0x7e, 0x7f, 0x80, 0xff, 0x5c})

	tests := []struct {
		name       string
		getSetting func(name string) (string, bool)
		want       string
	}{
		{"nil session falls back to hex", nil, `\x00011f207e7f80ff5c`},
		{"hex explicit", byteaOutputSetting("hex"), `\x00011f207e7f80ff5c`},
		{"unset GUC falls back to hex", func(string) (string, bool) { return "", false }, `\x00011f207e7f80ff5c`},
		{"escape", byteaOutputSetting("escape"), `\000\001\037 ~\177\200\377\\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(srv.appendTypedCellText(nil, d, byteaType, tt.getSetting))
			if got != tt.want {
				t.Errorf("appendTypedCellText(bytea, %s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestAppendTypedCellTextByteaPerSession proves the `bytea_output` mode is
// resolved per-call from getSetting, not a package-level global: two
// "sessions" rendering the SAME datum with different settings must produce
// different text, and neither call may leak into the other — the failure
// mode a global would pass a single-session test suite while still getting
// concurrent sessions wrong. M0134-0001 S12.
func TestAppendTypedCellTextByteaPerSession(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	byteaType := catalog.Type{Name: "bytea"}
	d := executor.NewBytesDatum([]byte{0x00, 0x5c})

	hexSession := byteaOutputSetting("hex")
	escapeSession := byteaOutputSetting("escape")

	gotHex := string(srv.appendTypedCellText(nil, d, byteaType, hexSession))
	gotEscape := string(srv.appendTypedCellText(nil, d, byteaType, escapeSession))
	// Re-render the hex session AFTER the escape session ran, to catch a
	// global that would have been mutated by the escape call.
	gotHexAgain := string(srv.appendTypedCellText(nil, d, byteaType, hexSession))

	if gotHex != `\x005c` {
		t.Errorf("hex session = %q, want %q", gotHex, `\x005c`)
	}
	if gotEscape != `\000\\` {
		t.Errorf("escape session = %q, want %q", gotEscape, `\000\\`)
	}
	if gotHexAgain != gotHex {
		t.Errorf("hex session rendered %q before the escape session and %q after — bytea_output leaked across sessions", gotHex, gotHexAgain)
	}
}

// byteaOutputSetting builds a getSetting closure that answers ONLY
// "bytea_output" — the sibling of constSetting (which answers "datestyle")
// above, kept separate because a single closure that answered every GUC name
// with the same value would let a name-check bug in appendTypedCellText's
// bytea case pass by accident.
func byteaOutputSetting(v string) func(name string) (string, bool) {
	return func(name string) (string, bool) {
		if name == "bytea_output" {
			return v, true
		}
		return "", false
	}
}

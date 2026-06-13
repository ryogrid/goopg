package initdb

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// TestBuildPgControlDataChecksumVersion verifies buildPgControl writes
// data_checksum_version (offset 252) as 1 when checksums are requested and
// 0 otherwise — the field pg_controldata reports as "Data page checksum
// version".
func TestBuildPgControlDataChecksumVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		wantVers uint32
	}{
		{"disabled", false, 0},
		{"enabled", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := buildPgControl(0xDEADBEEF, time.Now(), nil, tc.enabled)
			got := binary.LittleEndian.Uint32(buf[252:])
			if got != tc.wantVers {
				t.Fatalf("data_checksum_version = %d, want %d", got, tc.wantVers)
			}
		})
	}
}

// TestInitRejectsDataChecksums confirms Init refuses --data-checksums until
// the bootstrap page-write sites set pd_checksum (M0102-0010). It must fail
// before creating the data directory so no half-checksummed cluster is left
// behind.
func TestInitRejectsDataChecksums(t *testing.T) {
	dir := t.TempDir() + "/cluster"
	err := Init(Options{DataDir: dir, DataChecksums: true})
	if err == nil {
		t.Fatal("Init with DataChecksums=true should fail, got nil")
	}
	if !strings.Contains(err.Error(), "data-checksums") {
		t.Fatalf("error should mention data-checksums, got: %v", err)
	}
}

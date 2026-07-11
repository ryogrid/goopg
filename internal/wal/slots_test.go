package wal

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// TestSlotsCreateGetList pins the basic CRUD shape: creating a slot
// returns the snapshot, Get retrieves it, and List orders by name.
func TestSlotsCreateGetList(t *testing.T) {
	s, err := OpenSlots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatalf("Create primary: %v", err)
	}
	if _, err := s.Create("backup", SlotPhysical, 0x200); err != nil {
		t.Fatalf("Create backup: %v", err)
	}
	got, err := s.Get("primary")
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if got.RestartLSN != 0x100 || got.ConfirmedFlushLSN != 0x100 {
		t.Errorf("primary LSNs = (%x, %x), want (0x100, 0x100)",
			got.RestartLSN, got.ConfirmedFlushLSN)
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "backup" || list[1].Name != "primary" {
		t.Errorf("List = %+v, want [backup, primary]", list)
	}
}

// TestSlotsDuplicateRejected: a second Create with the same name must
// fail with ErrSlotExists, not silently overwrite the original.
func TestSlotsDuplicateRejected(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create("primary", SlotPhysical, 0x200)
	if !errors.Is(err, ErrSlotExists) {
		t.Errorf("dup Create err = %v, want ErrSlotExists", err)
	}
}

// TestSlotsInvalidName: upstream slot-name rules restrict to
// [a-z0-9_]{1,63}. Reject anything else.
func TestSlotsInvalidName(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	cases := []string{"", "Has-Dash", "UPPER", "with space", "ümlaut"}
	for _, name := range cases {
		if _, err := s.Create(name, SlotPhysical, 0); !errors.Is(err, ErrSlotInvalidName) {
			t.Errorf("Create(%q) err = %v, want ErrSlotInvalidName", name, err)
		}
	}
}

// TestSlotsDropAndActiveGuard: an active slot can't be dropped (would
// pull WAL out from under a connected walsender); inactive drops
// succeed.
func TestSlotsDropAndActiveGuard(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive("primary", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := s.Drop("primary"); !errors.Is(err, ErrSlotInUse) {
		t.Errorf("Drop active err = %v, want ErrSlotInUse", err)
	}
	if err := s.SetActive("primary", false); err != nil {
		t.Fatalf("SetActive false: %v", err)
	}
	if err := s.Drop("primary"); err != nil {
		t.Errorf("Drop inactive err = %v, want nil", err)
	}
	if _, err := s.Get("primary"); !errors.Is(err, ErrSlotNotFound) {
		t.Errorf("post-drop Get err = %v, want ErrSlotNotFound", err)
	}
}

// TestSlotsAdvanceLSN: AdvanceConfirmedFlushLSN moves both
// ConfirmedFlushLSN and RestartLSN forward but never backward.
// MinRestartLSN reflects the minimum across the registry.
func TestSlotsAdvanceLSN(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	_, _ = s.Create("a", SlotPhysical, 0x100)
	_, _ = s.Create("b", SlotPhysical, 0x200)
	if err := s.AdvanceConfirmedFlushLSN("a", 0x150); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceConfirmedFlushLSN("a", 0x140); err != nil {
		t.Fatal(err) // no-op, but non-error
	}
	got, _ := s.Get("a")
	if got.ConfirmedFlushLSN != 0x150 || got.RestartLSN != 0x150 {
		t.Errorf("after advance, a = (%x, %x), want (0x150, 0x150)",
			got.ConfirmedFlushLSN, got.RestartLSN)
	}
	if min := s.MinRestartLSN(); min != 0x150 {
		t.Errorf("MinRestartLSN = %x, want 0x150 (a's value)", min)
	}
}

// TestSlotsPersistence: after Create, OpenSlots on the same dir
// recovers the slot, validating the on-disk JSON layout.
func TestSlotsPersistence(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Create("primary", SlotPhysical, 0xCAFEBABE); err != nil {
		t.Fatal(err)
	}
	if err := s1.AdvanceConfirmedFlushLSN("primary", 0xDEADBEEF); err != nil {
		t.Fatal(err)
	}
	// File layout: <dir>/pg_replslot/primary/state
	statePath := filepath.Join(dir, "pg_replslot", "primary", "state")
	if _, err := readSlotFile(filepath.Dir(statePath)); err != nil {
		t.Fatalf("on-disk slot: %v", err)
	}
	s2, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("primary")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfirmedFlushLSN != 0xDEADBEEF || got.RestartLSN != 0xDEADBEEF {
		t.Errorf("rehydrated primary = %+v, want LSNs 0xDEADBEEF", got)
	}
	// Active is in-memory only; must not have persisted.
	if got.Active {
		t.Errorf("rehydrated primary.Active = true, want false")
	}
}

// TestCreateLogicalSlot pins the M0008 / 0008-0001 contract: a
// logical slot is created with the right Kind / Plugin / Database,
// participates in MinRestartLSN, and round-trips through the
// per-slot state file.
func TestCreateLogicalSlot(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLogical("logical1", "pgoutput", "appdb", 0x500); err != nil {
		t.Fatalf("CreateLogical: %v", err)
	}
	got, err := s.Get("logical1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != SlotLogical {
		t.Errorf("Kind=%q want %q", got.Kind, SlotLogical)
	}
	if got.Plugin != "pgoutput" || got.Database != "appdb" {
		t.Errorf("Plugin=%q Database=%q want (pgoutput, appdb)", got.Plugin, got.Database)
	}
	if got.RestartLSN != 0x500 || got.ConfirmedFlushLSN != 0x500 {
		t.Errorf("LSNs=(%x,%x) want (0x500,0x500)", got.RestartLSN, got.ConfirmedFlushLSN)
	}

	// Mix a physical slot in; both contribute to MinRestartLSN.
	if _, err := s.Create("phys1", SlotPhysical, 0x800); err != nil {
		t.Fatal(err)
	}
	if min := s.MinRestartLSN(); min != 0x500 {
		t.Errorf("MinRestartLSN=%x want 0x500 (logical pinning lower)", min)
	}

	// Reopen the registry — durable state must round-trip.
	s2, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := s2.Get("logical1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Kind != SlotLogical || got2.Plugin != "pgoutput" || got2.Database != "appdb" {
		t.Errorf("reopened logical slot lost fields: %+v", got2)
	}
}

// TestCreateLogicalRequiresPluginAndDatabase: empty plugin or
// database must fail at Create time so an operator sees the
// configuration error before any walsender attaches.
func TestCreateLogicalRequiresPluginAndDatabase(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	if _, err := s.CreateLogical("nopluging", "", "appdb", 0); !errors.Is(err, ErrSlotKindMismatch) {
		t.Errorf("empty plugin err=%v want ErrSlotKindMismatch", err)
	}
	if _, err := s.CreateLogical("nodb", "pgoutput", "", 0); !errors.Is(err, ErrSlotKindMismatch) {
		t.Errorf("empty database err=%v want ErrSlotKindMismatch", err)
	}
}

// TestPhysicalSlotJSONUnchangedAcrossM0008 pins the wire-format
// forward-compat: a physical slot's persisted state has no
// `plugin`/`database`/`catalog_xmin` keys (omitempty), so a
// pre-M0008 slot file round-trips through reopen without the new
// fields appearing.
// TestSlotBinaryMagicVersionCRC verifies that a freshly written slot state
// file carries the correct PG magic, version, and CRC32C checksum.
func TestSlotBinaryMagicVersionCRC(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSlots(dir)
	if _, err := s.Create("phys1", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "pg_replslot", "phys1", "state"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < slotOnDiskSize {
		t.Fatalf("state file too short: %d bytes", len(body))
	}

	magic := binary.LittleEndian.Uint32(body[slotOffMagic:])
	if magic != slotMagic {
		t.Errorf("magic = 0x%x, want 0x%x", magic, slotMagic)
	}

	ver := binary.LittleEndian.Uint32(body[slotOffVersion:])
	if ver != slotVersion {
		t.Errorf("version = %d, want %d", ver, slotVersion)
	}

	stored := binary.LittleEndian.Uint32(body[slotOffChecksum:])
	computed := crc32.Checksum(body[slotChecksumFrom:slotOnDiskSize], pgSlotCRCTable)
	if stored != computed {
		t.Errorf("CRC mismatch: stored=0x%08x computed=0x%08x", stored, computed)
	}
}



// TestSlotsMinCatalogXmin pins the retention-consumer contract: MinCatalogXmin
// aggregates the smallest non-zero catalog_xmin across non-invalidated slots
// and skips physical slots (catalog_xmin==0), freshly created logical slots
// (catalog_xmin==0), and invalidated slots. This is the value the vacuum/prune
// horizon is held back to via mvcc.Manager.SetCatalogXminSource.
func TestSlotsMinCatalogXmin(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())

	// No slots: nothing pinned.
	if got := s.MinCatalogXmin(); got != 0 {
		t.Fatalf("MinCatalogXmin (empty) = %d, want 0", got)
	}

	// Physical slot never pins a catalog horizon.
	if _, err := s.Create("phys", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	// Fresh logical slot has catalog_xmin==0 until the decoder reserves one.
	if _, err := s.CreateLogical("lfresh", "pgoutput", "appdb", 0x200); err != nil {
		t.Fatal(err)
	}
	if got := s.MinCatalogXmin(); got != 0 {
		t.Fatalf("MinCatalogXmin (phys + fresh logical) = %d, want 0", got)
	}

	// Two logical slots with distinct catalog_xmin — the minimum wins.
	if _, err := s.CreateLogical("l1", "pgoutput", "appdb", 0x300); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLogical("l2", "pgoutput", "appdb", 0x400); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCatalogXmin("l1", 750); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCatalogXmin("l2", 600); err != nil {
		t.Fatal(err)
	}
	if got := s.MinCatalogXmin(); got != 600 {
		t.Fatalf("MinCatalogXmin = %d, want 600 (l2's value)", got)
	}

	// Advancing l2 forward past l1 shifts the minimum to l1.
	if err := s.AdvanceCatalogXmin("l2", 800); err != nil {
		t.Fatal(err)
	}
	if got := s.MinCatalogXmin(); got != 750 {
		t.Fatalf("MinCatalogXmin after advance = %d, want 750 (l1's value)", got)
	}
}

// TestSlotsAdvanceCatalogXminMonotonicAndDurable pins two properties of the
// producer setter: catalog_xmin only moves forward (a smaller/equal xid is a
// no-op), and the advanced value is persisted so it survives a restart —
// otherwise catalog tuples a not-yet-reconnected decoder still needs could be
// pruned in the reconnection gap.
func TestSlotsAdvanceCatalogXminMonotonicAndDurable(t *testing.T) {
	dir := t.TempDir()
	s1, _ := OpenSlots(dir)
	if _, err := s1.CreateLogical("ls", "pgoutput", "appdb", 0x500); err != nil {
		t.Fatal(err)
	}
	if err := s1.AdvanceCatalogXmin("ls", 1000); err != nil {
		t.Fatal(err)
	}
	// Backward / equal are no-ops.
	if err := s1.AdvanceCatalogXmin("ls", 900); err != nil {
		t.Fatal(err)
	}
	if err := s1.AdvanceCatalogXmin("ls", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s1.AdvanceCatalogXmin("ls", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s1.Get("ls"); got.CatalogXmin != 1000 {
		t.Fatalf("CatalogXmin = %d, want 1000 (backward/zero ignored)", got.CatalogXmin)
	}
	// Unknown slot errors.
	if err := s1.AdvanceCatalogXmin("nope", 1); !errors.Is(err, ErrSlotNotFound) {
		t.Fatalf("AdvanceCatalogXmin(unknown) err = %v, want ErrSlotNotFound", err)
	}
	// Durability: reopen and confirm the value survived.
	s2, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Get("ls"); got.CatalogXmin != 1000 {
		t.Fatalf("CatalogXmin after reopen = %d, want 1000", got.CatalogXmin)
	}
	if min := s2.MinCatalogXmin(); min != 1000 {
		t.Fatalf("MinCatalogXmin after reopen = %d, want 1000", min)
	}
}

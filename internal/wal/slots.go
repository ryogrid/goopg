// Replication-slot store for streaming replication.
//
// A physical replication slot tracks one connected (or potentially
// reconnecting) standby's position in the WAL stream. The slot's
// `RestartLSN` is the oldest WAL byte position the standby may need
// to resume from after a reconnect; the WAL retention path consults
// `min(RestartLSN ∀ active slots)` to decide what's safe to recycle.
// `ConfirmedFlushLSN` tracks the last LSN the standby confirmed it
// has durably persisted (advances on each standby-status update).
//
// Slots persist to `<DataDir>/pg_replslot/<slot_name>/state` as a
// 200-byte PG-compatible binary file (see slots_pg.go), mirroring
// upstream's per-slot directory layout. Logical slots append a 64-byte
// goopg extension for the database name. The file is rewritten via
// tempfile + rename so a crash mid-update can't leave a torn record.
//
// See docs/design/0005-0001-streaming-replication-architecture.md.
package wal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SlotKind narrows what a slot is for. Physical slots support
// streaming replication (M0005); logical slots support
// logical-decoding-based replication (M0008). The shape is shared
// to keep retention paths uniform — `MinRestartLSN` /
// `InvalidateLagging` already work the same for both kinds.
type SlotKind string

const (
	SlotPhysical SlotKind = "physical"
	SlotLogical  SlotKind = "logical"
)

// Slot is the in-memory image of one replication slot.
type Slot struct {
	Name              string   `json:"name"`
	Kind              SlotKind `json:"kind"`
	RestartLSN        uint64   `json:"restart_lsn"`
	ConfirmedFlushLSN uint64   `json:"confirmed_flush_lsn"`
	// Invalidated, when true, marks the slot as no longer usable
	// because the WAL it was holding back was removed (the
	// standby's lag exceeded `max_slot_wal_keep_size`). An
	// invalidated slot persists so operators can `pg_drop_replication_slot`
	// it explicitly, but the retention path stops honouring its
	// RestartLSN and a reconnecting walsender will refuse to start
	// streaming. Mirrors upstream's "invalidated" state in
	// postgres/src/backend/replication/slot.c. See
	// docs/design/0005-0001-streaming-replication-architecture.md
	// (§ Replication slot retention semantics).
	Invalidated bool `json:"invalidated"`

	// Plugin names the output plugin a logical slot drives
	// (e.g. "pgoutput"). Empty for physical slots. Maps to
	// upstream's pg_replication_slots.plugin column. See
	// docs/design/0008-0001-logical-decoding-pipeline.md.
	Plugin string `json:"plugin,omitempty"`
	// Database is the database name a logical slot is anchored
	// in. Empty for physical slots (which span the whole cluster).
	// Maps to upstream's pg_replication_slots.database.
	Database string `json:"database,omitempty"`
	// CatalogXmin is the oldest transaction id whose catalog row
	// versions a logical slot may still need to reconstruct
	// historic snapshots for. Zero for physical slots and for
	// freshly created logical slots; populated by the decoder as
	// it advances. Maps to upstream's
	// pg_replication_slots.catalog_xmin.
	CatalogXmin uint64 `json:"catalog_xmin,omitempty"`

	// DatabaseOID is the PG Oid of the database a logical slot is
	// anchored in (0 = InvalidOid for physical slots). Stored in the
	// binary state file but not in the JSON fallback format.
	DatabaseOID uint32 `json:"-"`

	// Active is in-memory only; the durable state file records
	// the slot's existence, while Active reflects whether a
	// walsender connection currently owns it.
	Active bool `json:"-"`
}

// Slots is the process-wide registry. v0 keeps slots in a Go map
// guarded by a single RWMutex — the cardinality is small (default
// `max_replication_slots = 10`).
type Slots struct {
	dir string
	mu  sync.RWMutex
	m   map[string]*Slot
}

var (
	ErrSlotExists       = errors.New("replication slot already exists")
	ErrSlotNotFound     = errors.New("replication slot does not exist")
	ErrSlotInUse        = errors.New("replication slot is active")
	ErrSlotInvalidName  = errors.New("invalid replication slot name")
	ErrSlotMaxExceeded  = errors.New("max_replication_slots exceeded")
	ErrSlotKindMismatch = errors.New("replication slot kind mismatch")
)

// OpenSlots constructs a Slots registry rooted at `<DataDir>/pg_replslot/`.
// On startup it walks the directory and rehydrates every slot whose
// `state` file deserialises cleanly. Missing directories are created
// at mode 0700 (matches upstream's `pg_replslot/` permissions).
func OpenSlots(dataDir string) (*Slots, error) {
	root := filepath.Join(dataDir, "pg_replslot")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("slots: mkdir %s: %w", root, err)
	}
	s := &Slots{dir: root, m: make(map[string]*Slot)}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("slots: readdir %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slot, err := readSlotFile(filepath.Join(root, e.Name()))
		if err != nil {
			// Surface load errors instead of silently dropping the
			// slot — a corrupted slot file means WAL retention may
			// have lost the standby's anchor, and the operator
			// must decide how to recover.
			return nil, fmt.Errorf("slots: load %q: %w", e.Name(), err)
		}
		s.m[slot.Name] = slot
	}
	return s, nil
}

// Create persists a new slot. Returns ErrSlotExists if a slot with
// that name already exists. The new slot starts with RestartLSN =
// ConfirmedFlushLSN = startLSN; callers (CREATE_REPLICATION_SLOT
// handler) typically pass the current write LSN so retention only
// holds WAL produced from that point forward.
func (s *Slots) Create(name string, kind SlotKind, startLSN uint64) (*Slot, error) {
	if !validSlotName(name) {
		return nil, ErrSlotInvalidName
	}
	if kind != SlotPhysical && kind != SlotLogical {
		return nil, fmt.Errorf("%w: kind %q not recognised",
			ErrSlotKindMismatch, kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[name]; exists {
		return nil, ErrSlotExists
	}
	slot := &Slot{
		Name:              name,
		Kind:              kind,
		RestartLSN:        startLSN,
		ConfirmedFlushLSN: startLSN,
	}
	if err := s.writeSlotLocked(slot); err != nil {
		return nil, err
	}
	s.m[name] = slot
	// Return a copy so callers can't mutate the stored entry.
	out := *slot
	return &out, nil
}

// CreateLogical persists a new logical-decoding slot anchored in the
// named database and driven by the named output plugin. The slot
// participates in WAL retention via `MinRestartLSN` exactly the same
// way a physical slot does. Returns ErrSlotExists if a slot with
// `name` already exists. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func (s *Slots) CreateLogical(name, plugin, database string, startLSN uint64) (*Slot, error) {
	if !validSlotName(name) {
		return nil, ErrSlotInvalidName
	}
	if plugin == "" {
		return nil, fmt.Errorf("%w: plugin is required for logical slots",
			ErrSlotKindMismatch)
	}
	if database == "" {
		return nil, fmt.Errorf("%w: database is required for logical slots",
			ErrSlotKindMismatch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[name]; exists {
		return nil, ErrSlotExists
	}
	slot := &Slot{
		Name:              name,
		Kind:              SlotLogical,
		RestartLSN:        startLSN,
		ConfirmedFlushLSN: startLSN,
		Plugin:            plugin,
		Database:          database,
	}
	if err := s.writeSlotLocked(slot); err != nil {
		return nil, err
	}
	s.m[name] = slot
	out := *slot
	return &out, nil
}

// Drop removes a slot. Returns ErrSlotNotFound if it does not exist
// and ErrSlotInUse if a walsender currently owns it.
func (s *Slots) Drop(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.m[name]
	if !ok {
		return ErrSlotNotFound
	}
	if slot.Active {
		return ErrSlotInUse
	}
	if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("slots: remove %q: %w", name, err)
	}
	delete(s.m, name)
	return nil
}

// Get returns a copy of the named slot or ErrSlotNotFound.
func (s *Slots) Get(name string) (Slot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	slot, ok := s.m[name]
	if !ok {
		return Slot{}, ErrSlotNotFound
	}
	return *slot, nil
}

// List returns all slots in deterministic name order, copies of the
// stored values.
func (s *Slots) List() []Slot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.m))
	for name := range s.m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Slot, 0, len(names))
	for _, n := range names {
		out = append(out, *s.m[n])
	}
	return out
}

// AdvanceConfirmedFlushLSN updates the slot's ConfirmedFlushLSN to
// max(current, lsn). Callers (walsender on each standby status
// update) drive this. RestartLSN is also advanced to the same value
// for now; a fancier policy that lags RestartLSN behind by a
// configured retention would slot in here once max_slot_wal_keep_size
// enforcement lands.
func (s *Slots) AdvanceConfirmedFlushLSN(name string, lsn uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.m[name]
	if !ok {
		return ErrSlotNotFound
	}
	changed := false
	if lsn > slot.ConfirmedFlushLSN {
		slot.ConfirmedFlushLSN = lsn
		changed = true
	}
	if lsn > slot.RestartLSN {
		slot.RestartLSN = lsn
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeSlotLocked(slot)
}

// SetActive marks a slot as owned (or released) by a walsender.
// Returns ErrSlotNotFound if the slot doesn't exist and ErrSlotInUse
// if attempting to acquire a slot already held.
func (s *Slots) SetActive(name string, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.m[name]
	if !ok {
		return ErrSlotNotFound
	}
	if active && slot.Active {
		return ErrSlotInUse
	}
	slot.Active = active
	return nil
}

// MinRestartLSN returns the smallest RestartLSN across all slots that
// are still usable for WAL retention. Invalidated slots are skipped:
// the operator already accepted the lag-based eviction, so their
// stale RestartLSN must not pin WAL anymore. Returns 0 when no slots
// (or no live slots) exist — the WAL retention path then degenerates
// to "checkpoint horizon only", which is the v0 unbounded-by-slots
// case.
func (s *Slots) MinRestartLSN() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var min uint64
	first := true
	for _, slot := range s.m {
		if slot.Invalidated {
			continue
		}
		if first || slot.RestartLSN < min {
			min = slot.RestartLSN
			first = false
		}
	}
	return min
}

// MinCatalogXmin returns the smallest non-zero catalog_xmin pinned by
// any non-invalidated slot, or 0 when no slot currently pins a catalog
// horizon. A logical slot's catalog_xmin is the oldest transaction whose
// catalog row versions its decoder may still need to reconstruct a
// historic snapshot; the vacuum / opportunistic-prune horizon must not
// advance past it or catalog tuples could be reclaimed out from under an
// in-flight decode. Physical slots and freshly created logical slots have
// CatalogXmin==0 and are skipped, so they never pin the catalog horizon.
// Mirrors the catalog_xmin aggregation in upstream
// ReplicationSlotsComputeRequiredXmin (postgres/src/backend/replication/slot.c).
func (s *Slots) MinCatalogXmin() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var min uint64
	for _, slot := range s.m {
		if slot.Invalidated || slot.CatalogXmin == 0 {
			continue
		}
		if min == 0 || slot.CatalogXmin < min {
			min = slot.CatalogXmin
		}
	}
	return min
}

// AdvanceCatalogXmin pins (or advances) the named slot's catalog_xmin to
// xid and durably persists it. The value only moves forward — a smaller or
// equal xid is ignored — mirroring upstream's monotonic
// LogicalIncreaseXminForSlot (catalog_xmin is reserved at slot creation and
// then advanced as decoding confirms it no longer needs older catalog tuple
// versions; it never moves backward). xid==0 is a no-op. Returns
// ErrSlotNotFound for an unknown slot. Persisting the value means the horizon
// survives a restart, so catalog tuples a not-yet-reconnected logical decoder
// still needs are not pruned in the gap. Used by the logical-decoding pipeline
// to feed MinCatalogXmin, which the vacuum/prune horizon consults via
// mvcc.Manager.SetCatalogXminSource.
func (s *Slots) AdvanceCatalogXmin(name string, xid uint64) error {
	if xid == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, ok := s.m[name]
	if !ok {
		return ErrSlotNotFound
	}
	if xid <= slot.CatalogXmin {
		return nil
	}
	slot.CatalogXmin = xid
	return s.writeSlotLocked(slot)
}

// InvalidateLagging marks every non-invalidated slot whose lag
// (currentLSN - RestartLSN) exceeds maxKeepBytes as invalidated, and
// returns the names of the slots that were just invalidated. Callers
// (the slot-aware retention hook on the checkpointer) drive this
// before computing the keep horizon so a single laggy standby can't
// pin WAL forever.
//
// maxKeepBytes <= 0 disables the bound — no slot is ever invalidated.
// This matches upstream's `max_slot_wal_keep_size = -1` default
// (unlimited retention).
//
// currentLSN is typically the writer's WrittenLSN at checkpoint time;
// it must be ≥ every slot's RestartLSN for the lag computation to be
// meaningful (slots that somehow advanced past it are treated as
// zero-lag and left alone).
//
// Active slots are still invalidated — upstream behaves the same way:
// the next standby-status reply from the laggy walsender will fail
// because the slot's RestartLSN is now considered stale, and the
// operator must rebuild the standby from a fresh base backup. v0
// inherits that semantics.
func (s *Slots) InvalidateLagging(currentLSN uint64, maxKeepBytes int64) ([]string, error) {
	if maxKeepBytes <= 0 || currentLSN == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var invalidated []string
	for _, slot := range s.m {
		if slot.Invalidated {
			continue
		}
		if slot.RestartLSN >= currentLSN {
			continue
		}
		if currentLSN-slot.RestartLSN <= uint64(maxKeepBytes) {
			continue
		}
		slot.Invalidated = true
		if err := s.writeSlotLocked(slot); err != nil {
			// Roll back the in-memory flip so a transient I/O
			// failure doesn't strand the slot in a "logically
			// invalidated, durably alive" inconsistent state.
			slot.Invalidated = false
			return invalidated, err
		}
		invalidated = append(invalidated, slot.Name)
	}
	sort.Strings(invalidated)
	return invalidated, nil
}

// validSlotName mirrors upstream's check in
// postgres/src/backend/replication/slot.c (ReplicationSlotValidateName):
// non-empty, ≤63 bytes, only [a-z0-9_].
func validSlotName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// writeSlotLocked persists slot to <dir>/<slot.Name>/state via a
// tempfile + rename. Caller must hold s.mu.
func (s *Slots) writeSlotLocked(slot *Slot) error {
	slotDir := filepath.Join(s.dir, slot.Name)
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		return fmt.Errorf("slots: mkdir %s: %w", slotDir, err)
	}
	data := marshalSlotBinary(slot)
	tmp, err := os.CreateTemp(slotDir, "state.tmp-*")
	if err != nil {
		return fmt.Errorf("slots: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("slots: write %q: %w", slot.Name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("slots: sync %q: %w", slot.Name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("slots: close %q: %w", slot.Name, err)
	}
	finalName := filepath.Join(slotDir, "state")
	if err := os.Rename(tmpName, finalName); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("slots: rename %q: %w", slot.Name, err)
	}
	// Fsync state file and slot directory to make the rename durable,
	// mirroring upstream's SaveSlotToPath fsync sequence.
	if f, err := os.Open(finalName); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if d, err := os.Open(slotDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func readSlotFile(slotDir string) (*Slot, error) {
	statePath := filepath.Join(slotDir, "state")
	body, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	// Binary format: first byte is 0xA1 (low byte of slotMagic 0x01051CA1,
	// little-endian). JSON files start with '{' (0x7B).
	if len(body) >= slotOnDiskSize && body[0] != '{' {
		slot, err := unmarshalSlotBinary(body, filepath.Base(slotDir))
		if err != nil {
			return nil, fmt.Errorf("decode binary: %w", err)
		}
		return slot, nil
	}
	// JSON fallback for files written by older goopg versions.
	body = []byte(strings.TrimSpace(string(body)))
	var slot Slot
	if err := json.Unmarshal(body, &slot); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if slot.Name == "" {
		slot.Name = filepath.Base(slotDir)
	}
	if slot.Kind == "" {
		slot.Kind = SlotPhysical
	}
	return &slot, nil
}

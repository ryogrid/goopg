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
// small JSON file, mirroring upstream's per-slot directory layout.
// The file is rewritten via tempfile + rename so a crash mid-update
// can't leave a torn record.
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

// SlotKind narrows what a slot is for. v0 ships physical only;
// logical-replication slots are deferred per the milestone.
type SlotKind string

const (
	SlotPhysical SlotKind = "physical"
)

// Slot is the in-memory image of one replication slot.
type Slot struct {
	Name              string   `json:"name"`
	Kind              SlotKind `json:"kind"`
	RestartLSN        uint64   `json:"restart_lsn"`
	ConfirmedFlushLSN uint64   `json:"confirmed_flush_lsn"`
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
	if kind != SlotPhysical {
		return nil, fmt.Errorf("%w: only %q is supported in v0",
			ErrSlotKindMismatch, SlotPhysical)
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

// MinRestartLSN returns the smallest RestartLSN across all slots, or
// 0 if there are no slots. The WAL retention path consults this
// before recycling segments. Returning 0 (no slots) is the unbounded
// case — retention is then governed only by checkpoint policy and
// `max_wal_size`.
func (s *Slots) MinRestartLSN() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var min uint64
	first := true
	for _, slot := range s.m {
		if first || slot.RestartLSN < min {
			min = slot.RestartLSN
			first = false
		}
	}
	return min
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
	tmp, err := os.CreateTemp(slotDir, "state.tmp-*")
	if err != nil {
		return fmt.Errorf("slots: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(slot); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("slots: encode %q: %w", slot.Name, err)
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
	return nil
}

func readSlotFile(slotDir string) (*Slot, error) {
	statePath := filepath.Join(slotDir, "state")
	body, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
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

package transam

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// The prepared-transaction registry: twophase.c's TwoPhaseState, as far as
// goopg needs it. Each entry is a prepared transaction — its xid, committed
// subtransactions, gid, its two-phase state buffer (the XLOG_XACT_PREPARE
// main data) and where its PREPARE record ended. CheckPointTwoPhase moves the
// state of transactions prepared before a checkpoint's redo point into
// pg_twophase/ files; a restart restores them from those files and from the
// PREPARE records after redo (restoreTwoPhaseData / PrepareRedoAdd), so a
// prepared transaction survives until COMMIT PREPARED or ROLLBACK PREPARED.
// M0149-0015a.

// PreparedXact is one prepared transaction.
type PreparedXact struct {
	XID      storage.TransactionID
	Subxacts []storage.TransactionID
	GID      string
	// State is the two-phase state buffer without its CRC.
	State []byte
	// PrepareEndLSN is the end of the XLOG_XACT_PREPARE record.
	PrepareEndLSN uint64
	// OnDisk: the state is in pg_twophase/ (checkpointed or restored from a
	// file).
	OnDisk bool
}

type twoPhaseRegistry struct {
	mu      sync.Mutex
	byXID   map[storage.TransactionID]*PreparedXact
	subToXx map[storage.TransactionID]storage.TransactionID
	dir     string // <datadir>/pg_twophase, "" = no files
}

// TwoPhaseDirName is the data-directory subdirectory holding the state files.
const TwoPhaseDirName = "pg_twophase"

// TwoPhaseFileName is TwoPhaseFilePath's name for xid: the 64-bit full
// transaction id as two 8-digit hex words, epoch then xid. goopg is always in
// epoch 0.
func TwoPhaseFileName(xid storage.TransactionID) string {
	return fmt.Sprintf("%08X%08X", 0, uint32(xid))
}

// SetTwoPhaseDir points the registry at <datadir>/pg_twophase.
func (m *Manager) SetTwoPhaseDir(dir string) {
	m.twoPhase.mu.Lock()
	m.twoPhase.dir = dir
	m.twoPhase.mu.Unlock()
}

// RegisterPrepared records a prepared transaction (MarkAsPrepared): its
// later commit or abort record is COMMIT PREPARED / ABORT PREPARED, and the
// next checkpoint past PrepareEndLSN writes its state file.
func (m *Manager) RegisterPrepared(p PreparedXact) {
	r := &m.twoPhase
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byXID == nil {
		r.byXID = map[storage.TransactionID]*PreparedXact{}
		r.subToXx = map[storage.TransactionID]storage.TransactionID{}
	}
	cp := p
	r.byXID[p.XID] = &cp
	for _, s := range p.Subxacts {
		r.subToXx[s] = p.XID
	}
}

// TakeTwoPhase reports whether xid is a prepared transaction and forgets it:
// its commit or abort record is then COMMIT PREPARED / ABORT PREPARED
// (RecordTransactionCommitPrepared), not a plain one. The caller removes the
// state file once that record is written (RemoveTwoPhaseFile).
func (m *Manager) TakeTwoPhase(xid storage.TransactionID) bool {
	r := &m.twoPhase
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byXID[xid]
	if !ok {
		return false
	}
	delete(r.byXID, xid)
	for _, s := range p.Subxacts {
		delete(r.subToXx, s)
	}
	return true
}

// IsPreparedXID reports whether xid is a prepared transaction or one of its
// subtransactions: the startup sweep must leave such an xid in progress.
func (m *Manager) IsPreparedXID(xid storage.TransactionID) bool {
	r := &m.twoPhase
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byXID[xid]; ok {
		return true
	}
	_, ok := r.subToXx[xid]
	return ok
}

// PreparedXacts returns the registered prepared transactions in xid order.
func (m *Manager) PreparedXacts() []PreparedXact {
	r := &m.twoPhase
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PreparedXact, 0, len(r.byXID))
	for _, p := range r.byXID {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].XID < out[j].XID })
	return out
}

// RemoveTwoPhaseFile is RemoveTwoPhaseFile: drop xid's state file, if any,
// once its COMMIT/ABORT PREPARED record is written.
func (m *Manager) RemoveTwoPhaseFile(xid storage.TransactionID) {
	m.twoPhase.mu.Lock()
	dir := m.twoPhase.dir
	m.twoPhase.mu.Unlock()
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, TwoPhaseFileName(xid)))
	}
}

// CheckPointTwoPhase is CheckPointTwoPhase(redo_horizon): every prepared
// transaction whose PREPARE record ended at or before the checkpoint's redo
// point, and whose state is not yet on disk, is written to pg_twophase/ and
// fsynced — after the checkpoint the WAL holding its PREPARE may be
// recycled.
func (m *Manager) CheckPointTwoPhase(redo uint64) error {
	r := &m.twoPhase
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir == "" {
		return nil
	}
	wrote := false
	for _, p := range r.byXID {
		if p.OnDisk || p.PrepareEndLSN > redo {
			continue
		}
		if err := writeTwoPhaseFile(r.dir, p.XID, p.State); err != nil {
			return err
		}
		p.OnDisk = true
		wrote = true
	}
	if wrote {
		if d, err := os.Open(r.dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

// writeTwoPhaseFile is RecreateTwoPhaseFile: the state followed by its
// CRC-32C.
func writeTwoPhaseFile(dir string, xid storage.TransactionID, state []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	buf := make([]byte, 0, len(state)+4)
	buf = append(buf, state...)
	buf = binary.LittleEndian.AppendUint32(buf, crc32.Checksum(state, crc32.MakeTable(crc32.Castagnoli)))
	path := filepath.Join(dir, TwoPhaseFileName(xid))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("could not recreate file %q: %w", path, err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("could not write file %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("could not fsync file %q: %w", path, err)
	}
	return f.Close()
}

// ReadTwoPhaseFile reads a pg_twophase state file and checks its CRC
// (ReadTwoPhaseFile). It returns the state without the CRC.
func ReadTwoPhaseFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("incorrect size of file %q", path)
	}
	state, crc := b[:len(b)-4], binary.LittleEndian.Uint32(b[len(b)-4:])
	if crc32.Checksum(state, crc32.MakeTable(crc32.Castagnoli)) != crc {
		return nil, fmt.Errorf("calculated CRC checksum does not match value stored in file %q", path)
	}
	return state, nil
}

// RestorePrepared puts a prepared transaction found at startup back in the
// proc array (the dummy PGPROC of RecoverPreparedTransactions): it takes a
// dedicated slot holding its xid, so snapshots see it in progress; its
// subtransactions are linked to it; the next xid is moved past all of them
// (the 012 "nextXid handling" check). The returned Transaction is what a
// later COMMIT / ROLLBACK PREPARED finishes.
func (m *Manager) RestorePrepared(p PreparedXact) (Transaction, error) {
	newProc := int32(-1)
	for i := ConnSlotCount; i < len(m.procArray.slots); i++ {
		if m.procArray.slots[i].inTxn.CompareAndSwap(0, 1) {
			newProc = int32(i)
			break
		}
	}
	if newProc < 0 {
		return Transaction{}, fmt.Errorf("mvcc: no free process slots for prepared transaction %d", p.XID)
	}
	s := &m.procArray.slots[newProc]
	s.xid.Store(uint64(p.XID))
	s.xmin.Store(^uint64(0)) // a prepared transaction holds no snapshot
	s.isolation = int32(IsolationReadCommitted)
	high := p.XID
	for _, sub := range p.Subxacts {
		m.RegisterSubXid(sub, p.XID)
		high = max(high, sub)
	}
	if m.NextXID() <= high {
		m.SetNextXID(high + 1)
	}
	m.RegisterPrepared(p)
	return Transaction{Handle: TxnHandle(newProc + 1), XID: p.XID, Isolation: IsolationReadCommitted}, nil
}

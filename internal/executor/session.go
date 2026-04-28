package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/mvcc"
)

// Session stores per-connection state the Transaction operator needs
// to manage BEGIN/COMMIT/ROLLBACK.
type Session interface {
	IsolationLevel() mvcc.IsolationLevel
	InExplicitTransaction() bool
	CurrentTransaction() (mvcc.Transaction, mvcc.Snapshot, bool)
	BeginExplicitTransaction(tx mvcc.Transaction, snap mvcc.Snapshot)
	EndExplicitTransaction()
}

// BasicSession is a minimal Session implementation for the v0
// executor path.
type BasicSession struct {
	isolation mvcc.IsolationLevel
	inTx      bool
	tx        mvcc.Transaction
	snap      mvcc.Snapshot
}

// NewBasicSession constructs an explicit-transaction session state
// holder with READ COMMITTED as default isolation.
func NewBasicSession() *BasicSession {
	return &BasicSession{isolation: mvcc.IsolationReadCommitted}
}

// SetIsolationLevel updates the default isolation level used by BEGIN.
func (s *BasicSession) SetIsolationLevel(level mvcc.IsolationLevel) error {
	if level != mvcc.IsolationReadCommitted && level != mvcc.IsolationRepeatableRead {
		return fmt.Errorf("executor: unsupported isolation level %v", level)
	}
	s.isolation = level
	return nil
}

func (s *BasicSession) IsolationLevel() mvcc.IsolationLevel { return s.isolation }

func (s *BasicSession) InExplicitTransaction() bool { return s.inTx }

func (s *BasicSession) CurrentTransaction() (mvcc.Transaction, mvcc.Snapshot, bool) {
	if !s.inTx {
		return mvcc.Transaction{}, mvcc.Snapshot{}, false
	}
	return s.tx, s.snap.Clone(), true
}

func (s *BasicSession) BeginExplicitTransaction(tx mvcc.Transaction, snap mvcc.Snapshot) {
	s.tx = tx
	s.snap = snap.Clone()
	s.inTx = true
}

func (s *BasicSession) EndExplicitTransaction() {
	s.tx = mvcc.Transaction{}
	s.snap = mvcc.Snapshot{}
	s.inTx = false
}

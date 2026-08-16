package transam

import (
	"errors"
	"fmt"
)

// SerializationFailureSQLState is the SQLSTATE for serialization failure
// (`ERRCODE_T_R_SERIALIZATION_FAILURE`). PostgreSQL raises this when SSI
// detects a dangerous rw-conflict structure that cannot be allowed to
// commit. goopg returns it through SerializationFailureError so the
// executor wrapper can surface SQLSTATE 40001 to the client.
const SerializationFailureSQLState = "40001"

// SerializationFailureError is returned by PreCommitCheckForSerializationFailure
// when a SERIALIZABLE transaction must abort to prevent a dangerous
// structure in the rw-conflict graph from materialising into an
// anomaly. It mirrors PostgreSQL's `ereport(ERROR, ERRCODE_T_R_SERIALIZATION_FAILURE,
// ...)` in `predicate.c`.
//
// Callers in the executor recognise this error type, perform Rollback,
// and surface the SQLSTATE to the wire protocol.
type SerializationFailureError struct {
	// Reason is the upstream-style "Reason code" detail string. The
	// PostgreSQL message uses fixed phrases per detection site; goopg
	// reuses those phrases so test scaffolding written against upstream
	// errordetail can recognise the goopg variant verbatim.
	Reason string
}

func (e *SerializationFailureError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("could not serialize access due to read/write dependencies among transactions (%s)", e.Reason)
}

// PrimaryMessage returns the bare errmsg PostgreSQL's predicate.c emits for a
// serialization failure — with NO reason code appended. This is the line psql
// and isolationtester print on the `ERROR:` line; the reason belongs in DETAIL
// (see Detail). Wire-protocol callers must use this rather than Error(), whose
// parenthesised reason is for Go-side logging only.
func (e *SerializationFailureError) PrimaryMessage() string {
	return "could not serialize access due to read/write dependencies among transactions"
}

// Detail returns the upstream-style errdetail line ("Reason code: ....") that
// predicate.c attaches via errdetail_internal. isolationtester suppresses
// DETAIL, but psql and the wire protocol surface it, so keeping it faithful
// preserves parity for non-isolationtester clients.
func (e *SerializationFailureError) Detail() string {
	if e == nil || e.Reason == "" {
		return ""
	}
	return "Reason code: " + e.Reason + "."
}

// SQLSTATE returns the SQLSTATE code carried by this error. Provided so
// the executor's error-conversion layer can detect the typed error via
// a public method without type-asserting through the mvcc package.
func (e *SerializationFailureError) SQLSTATE() string {
	return SerializationFailureSQLState
}

// IsSerializationFailure reports whether err is (or wraps) a
// SerializationFailureError. Convenience wrapper so callers in
// executor/protocol layers do not need to import the typed pointer
// directly.
func IsSerializationFailure(err error) bool {
	var sfe *SerializationFailureError
	return errors.As(err, &sfe)
}

// PreCommitCheckForSerializationFailure is the goopg analogue of
// PostgreSQL's `PreCommit_CheckForSerializationFailure`
// (`postgres/src/backend/storage/lmgr/predicate.c`). It runs the
// dangerous-structure scan over the rw-conflict graph reachable from
// the committing SERIALIZABLE transaction, and returns a typed
// *SerializationFailureError if the commit must be rejected with
// SQLSTATE 40001.
//
// Algorithm (mirrors upstream):
//
//  1. If the committing xact has been doomed (by an earlier peer's
//     dangerous-structure detection or a future on-conflict check),
//     fail immediately with reason "Canceled on identification as a
//     pivot, during commit attempt".
//
//  2. Otherwise, walk my `inConflicts` slice. Each entry is a pivot
//     candidate — the structure is `Tin -> Tpivot -> Me`, my
//     `inConflicts` are pivots that read what I wrote.
//
//     - Skip pivots that are already finished (`FinishedAt != 0`) or
//     doomed: they cannot create a new anomaly.
//
//     - Walk the pivot's `inConflicts` for a `Tin` candidate. If
//     `Tin == me` (the 2-cycle case — write-skew) or `Tin` is still
//     in-flight (the 3-cycle case — generic dangerous structure),
//     doom the pivot in place. The pivot will fail at its own
//     pre-commit attempt with SQLSTATE 40001.
//
//  3. The committing xact then proceeds; the caller calls finish()
//     which scrubs the dying xact's edges from every peer.
//
// The check is a no-op for RC/RR transactions (not registered in
// `ssiState.xacts`) and for read-only SERIALIZABLE transactions whose
// graph contains no edges. The first-slice substrate does not yet
// retain finished-but-conflict-relevant xacts past their FinishedAt
// stamping; that retention work is in scope for the post-commit half
// of M0104-0006 but the pre-commit check below is correct under both
// the current release-on-finish policy and the future deferred-cleanup
// policy because the dangerous-structure scan runs WHILE the
// committing xact is still in `ssiState.xacts` and BEFORE its peers
// are scrubbed — exactly the window upstream uses.
//
// Safe for concurrent use; takes m.ssiMu internally.
func (m *Manager) PreCommitCheckForSerializationFailure(handle TxnHandle) error {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.preCommitCheckForSerializationFailureLocked(handle)
}

// PrepareCheckForSerializationFailure runs the pre-commit dangerous-structure
// check at PREPARE TRANSACTION time (same-backend 2PC, M0118-0009) and, on
// success, marks the SerializableXact PREPARED. Upstream's
// PreCommit_CheckForSerializationFailure both performs the check and sets
// SXACT_FLAG_PREPARED (predicate.c line ~4773); goopg splits the two so a
// normal COMMIT never sets the flag (it finishes immediately and is gated out
// by FinishedAt). A PREPARED still-in-flight pivot cannot be doomed by a later
// committer — see the Prepared branch of
// preCommitCheckForSerializationFailureLocked.
//
// On a returned error the caller must abort the transaction; the flag is left
// unset. Safe for concurrent use.
func (m *Manager) PrepareCheckForSerializationFailure(handle TxnHandle) error {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if err := m.preCommitCheckForSerializationFailureLocked(handle); err != nil {
		return err
	}
	if m.ssiState.xacts != nil {
		if me, ok := m.ssiState.xacts[handle]; ok && me != nil {
			me.Prepared = true
			me.PrepareSeqNo = m.ssiState.nextCommitSeqNo
			m.ssiState.nextCommitSeqNo++
		}
	}
	return nil
}

func (m *Manager) preCommitCheckForSerializationFailureLocked(handle TxnHandle) error {
	if m.ssiState.xacts == nil {
		return nil
	}
	me, ok := m.ssiState.xacts[handle]
	if !ok || me == nil {
		// Not a SERIALIZABLE xact (RC/RR). Nothing to check.
		return nil
	}
	if me.Doomed {
		return &SerializationFailureError{
			Reason: "Canceled on identification as a pivot, during commit attempt",
		}
	}
	for _, pivot := range me.inConflicts {
		if pivot == nil {
			continue
		}
		if pivot.FinishedAt != InvalidCommitSeqNo {
			// Pivot already finished — its eventual outcome is fixed.
			// Mirrors the `!SxactIsCommitted(pivot)` upstream gate.
			continue
		}
		if pivot.Doomed {
			// Already targeted by an earlier scan — no need to walk again.
			continue
		}
		for _, tin := range pivot.inConflicts {
			if tin == nil {
				continue
			}
			dangerous := false
			if tin == me {
				// The 2-cycle case: write-skew between me and the pivot.
				// `farConflict->sxactOut == MySerializableXact` upstream.
				dangerous = true
			} else if tin.FinishedAt == InvalidCommitSeqNo && !tin.Doomed && !tin.ReadOnly {
				// The 3-cycle case: Tin is still in-flight, not doomed,
				// and not declared READ ONLY.
				// `!SxactIsCommitted(t0) && !SxactIsReadOnly(t0) &&
				// !SxactIsDoomed(t0)` upstream (predicate.c). A declared
				// READ ONLY Tin in-flight cannot complete the dangerous
				// structure here — it writes nothing, so it can still
				// resolve RO-safe — which is exactly the receipt-report
				// de-facto READ ONLY false-positive avoidance. M0118-0001.
				dangerous = true
			}
			if !dangerous {
				continue
			}
			if pivot.Prepared {
				// The pivot is already PREPARED (same-backend 2PC,
				// M0118-0009) — upstream cannot kill a prepared pivot
				// (it may yet COMMIT PREPARED and is durable on disk), so
				// the committing/preparing xact commits suicide instead.
				// `if (SxactIsPrepared(nearConflict->sxactOut)) ... ereport`
				// (predicate.c line ~4756). This is what makes the third
				// PREPARE TRANSACTION in prepared-transactions.spec fail on
				// itself rather than dooming the already-prepared pivot.
				return &SerializationFailureError{
					Reason: "Canceled on commit attempt with conflict in from prepared pivot",
				}
			}
			pivot.Doomed = true
			break
		}
	}
	return nil
}

// MarkDoomedForTest is a test-only mutator that sets the Doomed flag
// on the SerializableXact for handle. Mirrors PostgreSQL's
// `SXACT_FLAG_DOOMED` so we can exercise the "self is already doomed"
// branch of PreCommitCheckForSerializationFailure without driving a
// full dangerous-structure scan.
//
// Returns true if the flag was set, false if the handle is not
// registered. Not exported under a non-test name because production
// callers must reach the Doomed bit through the pre-commit scan.
func (m *Manager) MarkDoomedForTest(handle TxnHandle) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return false
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return false
	}
	sx.Doomed = true
	return true
}

// IsDoomedForTest reports whether the SerializableXact for handle has
// been doomed. Test-only diagnostic; production callers must consult
// the pre-commit scan's return value rather than this flag directly.
func (m *Manager) IsDoomedForTest(handle TxnHandle) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return false
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return false
	}
	return sx.Doomed
}

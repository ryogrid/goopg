// Package storage defines the CommandId type used for per-tuple cmin/cmax and
// the transaction-wide command counter. It mirrors PostgreSQL's CommandId
// (CommandId ≡ uint32 in transam.h).
package storage

// CommandId identifies a command (statement) within a transaction.
// The transaction starts at FirstCommandId (0); each statement that marks
// the counter as used causes the next CommandCounterIncrement to advance it.
// Mirrors PostgreSQL's CommandId (uint32, transam.h).
type CommandId uint32

const (
	// FirstCommandId is the command id at the start of a transaction.
	FirstCommandId CommandId = 0

	// InvalidCommandId is the all-bits-one sentinel (0xFFFFFFFF), mirroring
	// PostgreSQL's InvalidCommandId (~(CommandId)0). Used by callers that
	// operate without a command counter (VACUUM, tests) — it must be larger
	// than any real command id so that the cmin >= curcid self-visibility
	// check never hides a tuple when curcid is InvalidCommandId.
	// Distinct from FirstCommandId, unlike the pre-S8 goopg definition.
	InvalidCommandId CommandId = ^CommandId(0)
)

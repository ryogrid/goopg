package executor

import (
	"github.com/goopg/goopg/internal/storage"
)

// CommandCounter is goopg's transaction command counter, mirroring PostgreSQL's
// currentCommandId / currentCommandIdUsed in transam.c.
//
// The counter is per-connection (stored in Context) and starts at
// FirstCommandId (0). CommandCounterIncrement advances it by one only when
// the used flag is true — a lazy scheme that avoids counting read-only
// commands, postponing the 2³²-2 overflow limit (PG §
// src/backend/access/transam/xact.c CommandCounterIncrement).
//
// GetCurrentCommandId returns the current value; passing used=true marks the
// counter so the next CommandCounterIncrement actually advances. The executor
// stamps this value as cmin on inserted tuples and as cmax on deleted ones.
type CommandCounter struct {
	current storage.CommandId
	used    bool
}

// GetCurrentCommandId returns the current command id. When used is true, it
// marks the counter so that the next CommandCounterIncrement actually
// advances (matching PG's GetCurrentCommandId).
func (c *CommandCounter) GetCurrentCommandId(used bool) storage.CommandId {
	if used {
		c.used = true
	}
	return c.current
}

// CommandCounterIncrement advances the command counter by one when the used
// flag is set, then clears the flag. When used is false, this is a no-op —
// matching PG's lazy-advance scheme that skips read-only commands.
func (c *CommandCounter) CommandCounterIncrement() {
	if c.used {
		c.current++
		// Overflow guard: wrapping past 2³²-1 lands on InvalidCommandId (0).
		// PG errors here; goopg matches.
		if c.current == storage.FirstCommandId {
			// Wrapped — this would be 2³² commands in one transaction.
			// PG errors with "cannot have more than 2^32-2 commands in a transaction".
			// goopg matches: panic so the error path can convert this to an ereport.
			panic("CommandCounterIncrement: overflow (2^32 commands in one transaction)")
		}
		c.used = false
	}
}

// Reset returns the counter to its initial state (command 0, not used).
// Used in tests and when recycling a Context for a new transaction.
func (c *CommandCounter) Reset() {
	c.current = storage.FirstCommandId
	c.used = false
}

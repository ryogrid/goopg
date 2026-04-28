package executor

import "time"

// Context carries per-statement runtime state into every operator's
// Open / Next call. It is constructed by the wire-protocol path at
// statement start and torn down at statement end.
type Context struct {
	// Params holds bind values for $1, $2, ... — Params[i-1] is $i.
	Params []Datum
	// Now is the wall-clock value `current_timestamp` and friends
	// resolve to. Captured once at statement start so retries see
	// consistent values, matching upstream.
	Now time.Time
	// MaxRows caps the number of rows the executor produces. Zero
	// means unlimited. The extended-query protocol's Execute message
	// passes through here.
	MaxRows int
}

// NewContext builds a Context with sensible defaults: a fresh
// timestamp and no bind parameters. Tests use this directly.
func NewContext() *Context {
	return &Context{Now: time.Now()}
}

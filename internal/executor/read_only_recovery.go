package executor

// preventCommandDuringRecovery is ExecCheckXactReadOnly's effect during
// recovery: a hot standby runs every transaction read-only (XactReadOnly is
// forced while RecoveryInProgress), so a data-modifying statement fails
// with PreventCommandIfReadOnly's "cannot execute <cmd> in a read-only
// transaction" (25006). Temporary tables are exempt in PG, but a standby
// cannot create any. M0149-0077b.
func preventCommandDuringRecovery(ctx *Context, cmd string) error {
	if ctx == nil || !ctx.IsStandby {
		return nil
	}
	return &ExecError{Code: "25006", Message: "cannot execute " + cmd + " in a read-only transaction"}
}

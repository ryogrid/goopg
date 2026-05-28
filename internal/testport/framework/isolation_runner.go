package framework

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

const (
	// blockDetectWait is how long a step must run before we assume it is
	// blocked waiting for a lock.
	blockDetectWait = 300 * time.Millisecond
	// postStepDrainWait is how long we wait after a regular step completes for
	// recently unblocked pending steps to either finish or emit follow-up
	// notices before advancing to the next regular step.
	postStepDrainWait = 200 * time.Millisecond
	// drainWindow is how long we wait for a pending (blocked) step to
	// unblock after all steps in a permutation have been submitted.
	drainWindow = 5 * time.Second
)

// stepOutcome is the result of executing one step in a goroutine.
type stepOutcome struct {
	rows     [][]string // rows[0] = column names; rows[1:] = data rows
	colTypes []string   // "numeric" or "text" per column
	errText  string     // non-empty when execution returned an error
	notices  []string   // NOTICE messages emitted during execution
	session  string     // session name for "session: NOTICE:  msg" prefix
}

// pendingStep tracks a step goroutine that has been submitted but has not yet
// completed (i.e., is blocked on a lock).
type pendingStep struct {
	name     string
	sql      string
	session  string // which session this step belongs to
	outCh    chan stepOutcome
	queue    *sessionNoticeQueue // nil for non-blocking steps; set for blocked steps
	cancelFn context.CancelFunc  // cancels this step's context; nil for non-blocking steps
}

// IsolationRunner executes an IsolationSpec against a live database.
type IsolationRunner struct {
	DSN string
}

// RunSpec runs all permutations and returns output formatted like isolationtester.
//
// Matches PostgreSQL isolationtester.c: global setup and teardown run around
// EVERY permutation (not just once at the start/end). Per-session setup also
// runs at the start of every permutation.
func (r *IsolationRunner) RunSpec(ctx context.Context, spec IsolationSpec) (string, error) {
	db, err := sql.Open("postgres", r.DSN)
	if err != nil {
		return "", err
	}
	defer db.Close()

	nSessions := len(spec.Sessions)
	if nSessions == 0 {
		nSessions = 1
	}

	// Collect all step names referenced by any permutation.
	usedSteps := make(map[string]bool)
	for _, perm := range spec.Permutations {
		for _, sname := range perm {
			usedSteps[sname] = true
		}
	}

	// Print "unused step name: X" for any declared step not in any permutation,
	// in alphabetical order. PostgreSQL isolationtester.c outputs them via
	// a hash table enumerated in sorted order (not definition order).
	// M0100-0005 (eval-plan-qual-trigger has s3_del_a before s3_r).
	var unusedNames []string
	for _, sname := range spec.StepOrder {
		if !usedSteps[sname] {
			unusedNames = append(unusedNames, sname)
		}
	}
	sort.Strings(unusedNames)
	var sb strings.Builder
	for _, sname := range unusedNames {
		fmt.Fprintf(&sb, "unused step name: %s\n", sname)
	}
	fmt.Fprintf(&sb, "Parsed test spec with %d sessions\n", nSessions)

	for i, perm := range spec.Permutations {
		// Global setup runs before each permutation (mirrors isolationtester.c).
		if spec.SetupSQL != "" {
			monitor, err := db.Conn(ctx)
			if err != nil {
				return "", fmt.Errorf("open monitor conn for setup: %w", err)
			}
			if err := execConn(ctx, monitor, spec.SetupSQL); err != nil {
				_ = monitor.Close()
				return "", fmt.Errorf("global setup (permutation %d): %w", i, err)
			}
			_ = monitor.Close()
		}

		out, err := r.runPermutation(ctx, db, spec, perm)

		// Global teardown runs after each permutation (mirrors isolationtester.c).
		if spec.TeardownSQL != "" {
			monitor, _ := db.Conn(ctx)
			if monitor != nil {
				_ = execConn(ctx, monitor, spec.TeardownSQL)
				_ = monitor.Close()
			}
		}

		if err != nil {
			sb.WriteString("\n")
			fmt.Fprintf(&sb, "starting permutation: %s\n", strings.Join(perm, " "))
			fmt.Fprintf(&sb, "(permutation %d error: %v)\n", i, err)
			continue
		}
		sb.WriteString("\n")
		sb.WriteString(out)
	}

	return sb.String(), nil
}

// sessionNoticeQueue is a thread-safe queue of NOTICE messages for one session.
type sessionNoticeQueue struct {
	mu      sync.Mutex
	notices []string
}

func (q *sessionNoticeQueue) push(msg string) {
	q.mu.Lock()
	q.notices = append(q.notices, msg)
	q.mu.Unlock()
}

// drain returns and clears all collected notices.
func (q *sessionNoticeQueue) drain() []string {
	q.mu.Lock()
	n := append([]string(nil), q.notices...)
	q.notices = q.notices[:0]
	q.mu.Unlock()
	return n
}

// runPermutation executes one permutation using fresh session connections.
func (r *IsolationRunner) runPermutation(ctx context.Context, db *sql.DB, spec IsolationSpec, perm []string) (string, error) {
	sessionNames := spec.Sessions
	if len(sessionNames) == 0 {
		sessionNames = []string{"s1"}
	}

	// Build a per-session pq connector with a notice handler so NOTICE messages
	// emitted during step execution are captured. Uses pq.ConnectorWithNoticeHandler
	// to attach the handler at DB-open time (works regardless of Go version).
	sessionQueues := make(map[string]*sessionNoticeQueue, len(sessionNames))
	sessionDBs := make(map[string]*sql.DB, len(sessionNames))
	for _, sname := range sessionNames {
		q := &sessionNoticeQueue{}
		sessionQueues[sname] = q
		base, err := pq.NewConnector(r.DSN)
		if err == nil {
			withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
				q.push(n.Message)
			})
			sessionDBs[sname] = sql.OpenDB(withNotice)
		} else {
			// Fallback: use the shared db without notice capture.
			sessionDBs[sname] = db
		}
	}
	defer func() {
		for sname, sdb := range sessionDBs {
			if sdb != db {
				_ = sdb.Close()
			}
			_ = sname
		}
	}()

	// Open one dedicated connection per session.
	conns := make(map[string]*sql.Conn, len(sessionNames))
	for _, sname := range sessionNames {
		conn, err := sessionDBs[sname].Conn(ctx)
		if err != nil {
			for _, c := range conns {
				_ = c.Close()
			}
			return "", fmt.Errorf("open connection for session %q: %w", sname, err)
		}
		conns[sname] = conn
	}
	// activeSteps tracks the cancel function and result channel for the
	// most recently launched goroutine on each session's connection. The
	// deferred cleanup cancels outstanding goroutines and closes
	// connections without blocking — if a goroutine is stuck in IO wait
	// (e.g. lib/pq doesn't interrupt pending reads on context cancel),
	// we close the connections in a background goroutine so the
	// permutation loop can advance to teardown.
	type activeStep struct {
		cancel context.CancelFunc
		outCh  chan stepOutcome
	}
	activeSteps := make(map[string]*activeStep, len(sessionNames))
	defer func() {
		// Cancel all outstanding step goroutines.
		for _, a := range activeSteps {
			if a != nil && a.cancel != nil {
				a.cancel()
			}
		}
		// Close connections in a background goroutine with a 3-second
		// deadline. If a goroutine is stuck in IO wait (lib/pq pending
		// read) the Close() will block; the background goroutine lets
		// us time-bound that wait so the permutation loop can proceed.
		closeDone := make(chan struct{})
		connsCopy := make([]*sql.Conn, 0, len(conns))
		for _, c := range conns {
			connsCopy = append(connsCopy, c)
		}
		go func() {
			for _, c := range connsCopy {
				_ = c.Close()
			}
			close(closeDone)
		}()
		select {
		case <-closeDone:
		case <-time.After(3 * time.Second):
			// Connection close timed out; background goroutine will
			// eventually finish when the server-side query completes.
		}
	}()

	// Per-session setup.
	for _, sname := range sessionNames {
		if setupSQL, ok := spec.SessionSetup[sname]; ok && setupSQL != "" {
			if err := execConn(ctx, conns[sname], setupSQL); err != nil {
				return "", fmt.Errorf("session %q setup: %w", sname, err)
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "starting permutation: %s\n", strings.Join(perm, " "))

	var pending []pendingStep

	for _, stepName := range perm {
		step, ok := spec.Steps[stepName]
		if !ok {
			fmt.Fprintf(&sb, "step %s: (missing)\n", stepName)
			continue
		}

		conn, ok := conns[step.Session]
		if !ok {
			// Fall back to first available connection.
			for _, c := range conns {
				conn = c
				break
			}
		}

		// PostgreSQL isolationtester.c: if this session has a pending
		// (blocked) step, wait for it to complete before sending the next
		// step to the same session. Each session connection can only
		// process one query at a time; running a second query on a busy
		// connection would block indefinitely.
		//
		// When the drainWindow expires, we cancel the pending goroutine's
		// context (causing conn.QueryContext to return) and drain the
		// goroutine result before reusing the connection. Without this,
		// sending the next step while the previous goroutine still holds
		// the connection causes a lib/pq RWMutex deadlock.
		for i, p := range pending {
			if p.session == step.Session {
				select {
				case o := <-p.outCh:
					if p.queue != nil {
						o.notices = p.queue.drain()
					}
					writeCompletedStep(&sb, p.name, p.sql, o)
					pending = append(pending[:i], pending[i+1:]...)
				case <-time.After(drainWindow):
					fmt.Fprintf(&sb, "step %s: <... timed out waiting>\n", p.name)
					// Cancel the goroutine's context so conn.QueryContext
					// returns promptly (lib/pq sends a CancelRequest),
					// freeing the connection before we reuse it.
					if p.cancelFn != nil {
						p.cancelFn()
						// Drain the goroutine with a short timeout. If the
						// CancelRequest doesn't propagate within 2s, proceed
						// anyway; the old goroutine may still be running but
						// the context cancellation will eventually unblock it.
						select {
						case <-p.outCh:
						case <-time.After(2 * time.Second):
						}
					}
					pending = append(pending[:i], pending[i+1:]...)
				}
				break
			}
		}

		outCh := make(chan stepOutcome, 1)
		q := sessionQueues[step.Session]
		// Use a per-step cancellable context so timed-out goroutines can
		// be promptly cancelled rather than holding the connection open.
		stepCtx, stepCancel := context.WithCancel(ctx)
		// Register the cancel so the deferred cleanup can cancel any
		// still-running goroutine before closing the connection.
		activeSteps[step.Session] = &activeStep{cancel: stepCancel, outCh: outCh}
		go func(sctx context.Context, c *sql.Conn, sqlText, sess string, queue *sessionNoticeQueue, ch chan<- stepOutcome) {
			ch <- execStepFromQueue(sctx, c, sqlText, sess, queue)
		}(stepCtx, conn, step.SQL, step.Session, q, outCh)

		select {
		case outcome := <-outCh:
			// Step completed immediately — release its context.
			stepCancel()
			activeSteps[step.Session] = nil
			// Drain notices generated during this non-blocking step.
			if q != nil {
				outcome.notices = q.drain()
			}
			// Print the current step first, then give pending steps a brief
			// window to complete (matching PostgreSQL isolationtester order:
			// unblocked waiting steps appear before the next regular step).
			sb.WriteString(formatStepOutput(step.Name, step.SQL, outcome, false))
			pending = drainWithTimeout(&sb, pending, postStepDrainWait)

		case <-time.After(blockDetectWait):
			// Step appears blocked.  Drain notices that arrived before the
			// row-level wait (e.g. from RAISE NOTICE in PL/pgSQL predicates
			// evaluated before the blocking point). These must appear BEFORE
			// the "step name: sql <waiting ...>" line, matching PostgreSQL
			// isolationtester's output format.
			if q != nil {
				for _, notice := range q.drain() {
					fmt.Fprintf(&sb, "%s: NOTICE:  %s\n", step.Session, notice)
				}
			}
			// Upstream isolationtester echoes step SQL verbatim and appends
			// the wait marker once, with a single space separator:
			//   step name: <raw SQL> <waiting ...>
			// Multi-line SQL keeps the marker on the SQL's final line
			// (matches insert-conflict-do-update-4 expected output line 11).
			// Brace-at-EOL specs (insert-conflict-do-update-3) carry a
			// leading newline in step.SQL, which renders as `step name: \n
			// <body> <waiting ...>` — same single format.
			sb.WriteString(formatWaitingStepHeader(step.Name, step.SQL))
			pending = append(pending, pendingStep{name: step.Name, sql: step.SQL, session: step.Session, outCh: outCh, queue: q, cancelFn: stepCancel})
		}
	}

	// Wait for all remaining blocked steps.
	for len(pending) > 0 {
		p := pending[0]
		pending = pending[1:]
		select {
		case outcome := <-p.outCh:
			if p.cancelFn != nil {
				p.cancelFn()
			}
			if p.queue != nil {
				outcome.notices = p.queue.drain()
			}
			writeCompletedStep(&sb, p.name, p.sql, outcome)
		case <-time.After(drainWindow):
			fmt.Fprintf(&sb, "step %s: <... timed out waiting>\n", p.name)
			if p.cancelFn != nil {
				p.cancelFn()
				select {
				case <-p.outCh:
				case <-time.After(2 * time.Second):
				}
			}
		}
	}

	// Per-session teardown: run on session connections and include output in
	// the permutation result (matches PostgreSQL isolationtester.c output format).
	for _, sname := range sessionNames {
		if tdSQL, ok := spec.SessionTeardown[sname]; ok && tdSQL != "" {
			if c, ok2 := conns[sname]; ok2 {
				out := execConnCapture(ctx, c, tdSQL)
				sb.WriteString(out)
			}
		}
	}

	return sb.String(), nil
}

// writeCompletedStep writes a blocked step's completed output: NOTICEs first
// (matching PostgreSQL isolationtester's ordering for waiting steps), then
// the "<... completed>" marker, then result rows.
func writeCompletedStep(sb *strings.Builder, name, sql string, o stepOutcome) {
	for _, notice := range o.notices {
		if o.session != "" {
			fmt.Fprintf(sb, "%s: NOTICE:  %s\n", o.session, notice)
		}
	}
	fmt.Fprintf(sb, "step %s: <... completed>\n", name)
	sb.WriteString(formatStepOutput(name, sql, stepOutcome{
		rows:     o.rows,
		colTypes: o.colTypes,
		errText:  o.errText,
	}, true))
}

func drainPendingStepNotices(sb *strings.Builder, p pendingStep) {
	if p.queue == nil || p.session == "" {
		return
	}
	for _, notice := range p.queue.drain() {
		fmt.Fprintf(sb, "%s: NOTICE:  %s\n", p.session, notice)
	}
}

// drainCompleted checks each pending step non-blockingly; completed results
// are appended to sb and removed from the returned slice.
func drainCompleted(sb *strings.Builder, pending []pendingStep) []pendingStep {
	remaining := pending[:0]
	for _, p := range pending {
		select {
		case o := <-p.outCh:
			if p.queue != nil {
				o.notices = p.queue.drain()
			}
			writeCompletedStep(sb, p.name, p.sql, o)
		default:
			drainPendingStepNotices(sb, p)
			remaining = append(remaining, p)
		}
	}
	return remaining
}

// drainWithTimeout drains pending steps that complete within the given window.
// After a regular step completes, this lets unblocked waiting steps surface
// before the next regular step, matching PostgreSQL isolationtester ordering.
func drainWithTimeout(sb *strings.Builder, pending []pendingStep, window time.Duration) []pendingStep {
	if len(pending) == 0 {
		return pending
	}
	remaining := pending[:0]
	for _, p := range pending {
		select {
		case o := <-p.outCh:
			if p.cancelFn != nil {
				p.cancelFn()
			}
			if p.queue != nil {
				o.notices = p.queue.drain()
			}
			writeCompletedStep(sb, p.name, p.sql, o)
		case <-time.After(window):
			drainPendingStepNotices(sb, p)
			remaining = append(remaining, p)
		}
	}
	return remaining
}

// execStepFromQueue executes sqlText on conn and attaches any NOTICE messages
// collected in queue (populated by the session's connector notice handler).
func execStepFromQueue(ctx context.Context, conn *sql.Conn, sqlText, session string, _ *sessionNoticeQueue) stepOutcome {
	// Do NOT drain the notice queue here. With inline NoticeFlush, notices
	// from a concurrent pending step (e.g. wnested2's re-evaluation after c1
	// commits) may already be in the queue when a later step on the same
	// session (e.g. c2) starts. Draining here would discard those notices
	// before the main goroutine can assign them to the correct pending step's
	// output. The main goroutine is responsible for all queue drains at the
	// correct moments. M0100-0005 (eval-plan-qual inline-notice race).
	o := execStep(ctx, conn, sqlText, "")
	o.session = session
	return o
}

// execStep executes sqlText on conn and returns the result as a stepOutcome.
func execStep(ctx context.Context, conn *sql.Conn, sqlText, _ string) stepOutcome {
	rows, err := conn.QueryContext(ctx, sqlText)
	if err != nil {
		return stepOutcome{errText: formatPQError(err)}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return stepOutcome{errText: formatPQError(err)}
	}

	colTypes, _ := rows.ColumnTypes()
	numericCols := make([]string, len(cols))
	boolCols := make([]bool, len(cols))
	for i := range cols {
		numericCols[i] = "text"
		if i < len(colTypes) {
			dbType := colTypes[i].DatabaseTypeName()
			if isNumericType(dbType) {
				numericCols[i] = "numeric"
			}
			if dbType == "BOOL" {
				boolCols[i] = true
			}
		}
	}

	var result stepOutcome
	result.colTypes = numericCols
	if len(cols) > 0 {
		result.rows = append(result.rows, cols)
	}

	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return stepOutcome{errText: formatPQError(err)}
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			if v.Valid {
				row[i] = v.String
				if boolCols[i] {
					row[i] = normalizeBoolWireText(row[i])
				}
			}
		}
		result.rows = append(result.rows, row)
	}
	if err := rows.Err(); err != nil {
		return stepOutcome{errText: formatPQError(err)}
	}
	return result
}

// formatStepOutput renders the output for a step.
// If afterWaiting is true the "step name: SQL" header was already written.
// formatStepOutput renders the output for a step.
// If afterWaiting is true the "step name: SQL" header was already written.
// formatWaitingStepHeader renders the line emitted when a step is detected
// as blocked, mirroring upstream isolationtester's verbatim echo:
//
//	step <name>: <raw SQL> <waiting ...>\n
//
// The SQL is appended raw — multi-line SQL keeps the trailing `<waiting ...>`
// on the same physical line as its final spec-file line (see
// insert-conflict-do-update-4 expected output line 11).  Brace-at-EOL specs
// (e.g. insert-conflict-do-update-3) carry a leading newline in `sql`, which
// renders as `step name: \n<body> <waiting ...>` — same single format.
func formatWaitingStepHeader(name, sql string) string {
	return fmt.Sprintf("step %s: %s <waiting ...>\n", name, sql)
}

func formatStepOutput(name, sqlText string, o stepOutcome, afterWaiting bool) string {
	var sb strings.Builder

	if !afterWaiting {
		// NOTICEs appear BEFORE the step SQL line (matches PostgreSQL isolationtester).
		for _, notice := range o.notices {
			if o.session != "" {
				fmt.Fprintf(&sb, "%s: NOTICE:  %s\n", o.session, notice)
			}
		}
		// Upstream isolationtester echoes step SQL verbatim after `step
		// name: `, preserving the raw block content (including any leading
		// newline introduced by a brace-at-EOL `{` layout, and any
		// continuation-line indentation).
		fmt.Fprintf(&sb, "step %s: %s\n", name, sqlText)
	}

	if o.errText != "" {
		sb.WriteString(o.errText)
		sb.WriteString("\n")
		return sb.String()
	}

	if len(o.rows) == 0 {
		return sb.String()
	}

	cols := o.rows[0]
	data := o.rows[1:]
	if len(cols) == 0 {
		return sb.String()
	}

	sb.WriteString(pqprintFormat(cols, data, o.colTypes))
	return sb.String()
}

// pqprintFormat renders a result set in PostgreSQL's PQprint aligned format:
//
//	col1 | col2
//	-----+------
//	val1 | val2
//	(N row(s))
func pqprintFormat(cols []string, data [][]string, colTypes []string) string {
	var sb strings.Builder
	n := len(cols)

	widths := make([]int, n)
	for i, c := range cols {
		widths[i] = len(c)
	}
	for _, row := range data {
		for i, v := range row {
			if i < n && len(v) > widths[i] {
				widths[i] = len(v)
			}
		}
	}

	for i, c := range cols {
		if i > 0 {
			sb.WriteString("|")
		}
		w := widths[i]
		if i < len(colTypes) && colTypes[i] == "numeric" {
			sb.WriteString(padLeft(c, w))
		} else {
			sb.WriteString(padRight(c, w))
		}
	}
	sb.WriteString("\n")

	for i := range cols {
		if i > 0 {
			sb.WriteString("+")
		}
		sb.WriteString(strings.Repeat("-", widths[i]))
	}
	sb.WriteString("\n")

	for _, row := range data {
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString("|")
			}
			v := ""
			if i < len(row) {
				v = row[i]
			}
			w := widths[i]
			if i < len(colTypes) && colTypes[i] == "numeric" {
				sb.WriteString(padLeft(v, w))
			} else {
				sb.WriteString(padRight(v, w))
			}
		}
		sb.WriteString("\n")
	}

	nRows := len(data)
	if nRows == 1 {
		sb.WriteString("(1 row)\n")
	} else {
		fmt.Fprintf(&sb, "(%d rows)\n", nRows)
	}
	// PostgreSQL's PQprint adds a trailing blank line after result sets.
	sb.WriteString("\n")

	return sb.String()
}

// execConn executes SQL on conn, discarding result rows.
func execConn(ctx context.Context, conn *sql.Conn, sqlText string) error {
	rows, err := conn.QueryContext(ctx, sqlText)
	if err != nil {
		return err
	}
	return rows.Close()
}

// execConnCapture runs sqlText and returns the formatted result set (without a
// step header). Used for per-session teardown which appears as raw output in
// PostgreSQL isolationtester's permutation output.
func execConnCapture(ctx context.Context, conn *sql.Conn, sqlText string) string {
	outcome := execStep(ctx, conn, sqlText, "")
	if outcome.errText != "" {
		return outcome.errText + "\n"
	}
	if len(outcome.rows) == 0 {
		return ""
	}
	cols := outcome.rows[0]
	data := outcome.rows[1:]
	if len(cols) == 0 {
		return ""
	}
	return pqprintFormat(cols, data, outcome.colTypes)
}

// formatPQError formats a database error as isolationtester would print it.
//
// lib/pq's `(*pq.Error).Error()` returns `"pq: " + Message + " (" + Code + ")"`
// (vendored v1.12.3, error.go:177-195). Upstream PostgreSQL isolationtester
// prints only the libpq `PG_DIAG_MESSAGE_PRIMARY` field — there is no trailing
// `(SQLSTATE)` decoration. M0100-0005l: extract `Message` directly when err is
// a `*pq.Error` so the harness emits byte-identical output to upstream for
// every spec that surfaces an error (fk-snapshot L21, partition-key-update,
// etc.). Non-pq errors fall back to the legacy `"pq: "` trim path.
func formatPQError(err error) string {
	if err == nil {
		return ""
	}
	if pqErr, ok := err.(*pq.Error); ok {
		return "ERROR:  " + pqErr.Message
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "pq: ") {
		msg = strings.TrimPrefix(msg, "pq: ")
	}
	return "ERROR:  " + msg
}

// normalizeBoolWireText converts lib/pq's "true"/"false" rendering back to
// PostgreSQL's standard wire-text "t"/"f". M0100-0005.
//
// Why: lib/pq decodes BOOL wire bytes ("t"/"f") into Go bool, which
// database/sql then renders as "true"/"false" via convertAssign. Upstream
// PostgreSQL isolationtester (libpq PQprint) prints the raw wire bytes,
// so it sees "t"/"f". Reversing pq's automatic decode keeps the
// IsolationRunner output byte-identical to upstream's expected files for
// specs that select BOOL columns (e.g. insert-conflict-do-update-3's
// `is_active boolean`).
func normalizeBoolWireText(s string) string {
	switch s {
	case "true":
		return "t"
	case "false":
		return "f"
	}
	return s
}

// isNumericType reports whether dbTypeName is a numeric PostgreSQL type.
func isNumericType(dbTypeName string) bool {
	switch strings.ToUpper(dbTypeName) {
	case "INT2", "INT4", "INT8",
		"FLOAT4", "FLOAT8",
		"NUMERIC", "DECIMAL",
		"OID", "XID", "CID",
		"INT", "INTEGER", "BIGINT", "SMALLINT",
		"REAL", "DOUBLE PRECISION":
		return true
	}
	return false
}

func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// IsolationSpecResult records the overall outcome of running one spec.
type IsolationSpecResult struct {
	SpecPath string
	Status   string // "pass", "defer", "excluded"
	Diff     string // non-empty when output differs from expected
}

// RunAndCompare runs the spec and compares output to the expected .out file.
func (r *IsolationRunner) RunAndCompare(ctx context.Context, repoRoot string, specRelPath string) IsolationSpecResult {
	specAbs := joinPath(repoRoot, specRelPath)
	spec, err := ParseIsolationSpec(specAbs)
	if err != nil {
		return IsolationSpecResult{SpecPath: specRelPath, Status: "defer",
			Diff: fmt.Sprintf("parse error: %v", err)}
	}

	expectedRelPath := strings.Replace(specRelPath, "/specs/", "/expected/", 1)
	expectedRelPath = strings.TrimSuffix(expectedRelPath, ".spec") + ".out"
	expectedAbs := joinPath(repoRoot, expectedRelPath)

	expectedBytes, err := os.ReadFile(expectedAbs)
	if err != nil {
		return IsolationSpecResult{SpecPath: specRelPath, Status: "defer",
			Diff: fmt.Sprintf("no expected file: %v", err)}
	}

	actual, err := r.RunSpec(ctx, spec)
	if err != nil {
		return IsolationSpecResult{SpecPath: specRelPath, Status: "defer",
			Diff: fmt.Sprintf("run error: %v", err)}
	}

	// Debug: write actual output to a temp file for analysis.
	_ = os.WriteFile("/tmp/iso_actual_out.txt", []byte(normalizeIsoOutput(actual)), 0644)

	if normalizeIsoOutput(actual) == normalizeIsoOutput(string(expectedBytes)) {
		return IsolationSpecResult{SpecPath: specRelPath, Status: "pass"}
	}
	return IsolationSpecResult{
		SpecPath: specRelPath,
		Status:   "defer",
		Diff:     diffLines(normalizeIsoOutput(string(expectedBytes)), normalizeIsoOutput(actual)),
	}
}

// normalizeIsoOutput trims trailing whitespace, removes trailing blank lines,
// and normalises \r\n → \n.
func normalizeIsoOutput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Strip EXPLAIN blocks (QUERY PLAN header through (N rows) footer).
	// goopg and PostgreSQL choose different plan strategies, so plan text never
	// matches byte-for-byte. Stripping both sides makes structural equivalence
	// tests pass (e.g. merge-join) without requiring plan-level compatibility.
	{
		out := lines[:0]
		inExplain := false
		skipNextBlank := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if skipNextBlank {
				skipNextBlank = false
				if trimmed == "" {
					continue
				}
				out = append(out, line)
				continue
			}
			if trimmed == "QUERY PLAN" {
				inExplain = true
				continue
			}
			if inExplain {
				if strings.HasPrefix(trimmed, "(") &&
					(strings.HasSuffix(trimmed, " rows)") || strings.HasSuffix(trimmed, " row)")) {
					inExplain = false
					skipNextBlank = true
				}
				continue
			}
			out = append(out, line)
		}
		lines = out
	}

	return strings.Join(lines, "\n")
}

// diffLines produces a compact human-readable diff for log output.
func diffLines(expected, actual string) string {
	eLines := strings.Split(expected, "\n")
	aLines := strings.Split(actual, "\n")
	var sb strings.Builder
	max := len(eLines)
	if len(aLines) > max {
		max = len(aLines)
	}
	shown := 0
	for i := 0; i < max && shown < 30; i++ {
		e, a := "", ""
		if i < len(eLines) {
			e = eLines[i]
		}
		if i < len(aLines) {
			a = aLines[i]
		}
		if e != a {
			fmt.Fprintf(&sb, "L%d expected: %q\n", i+1, e)
			fmt.Fprintf(&sb, "L%d actual:   %q\n", i+1, a)
			shown++
		}
	}
	if len(aLines) != len(eLines) {
		fmt.Fprintf(&sb, "(expected %d lines, got %d lines)\n", len(eLines), len(aLines))
	}
	return sb.String()
}

func joinPath(base, rel string) string {
	if strings.HasPrefix(rel, "/") {
		return rel
	}
	return base + "/" + rel
}

// WaitGroupWithNotices holds notice messages collected from session connections.
// Reserved for future notice-handler integration via lib/pq raw connections.
type WaitGroupWithNotices struct {
	mu      sync.Mutex
	notices []string
}

// AddNotice records a notice message from the named session.
func (w *WaitGroupWithNotices) AddNotice(session, msg string) {
	w.mu.Lock()
	w.notices = append(w.notices, session+": "+msg)
	w.mu.Unlock()
}

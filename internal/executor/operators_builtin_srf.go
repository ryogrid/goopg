package executor

// operators_builtin_srf.go — the executor side of optimizer.BuiltinSRFScan:
// built-in table functions that evaluate their arguments once per scan and
// produce all rows from a callback. Holds genfile.c's pg_ls_dir and
// pg_stat_file (M0149-0016c; recovery/t/009_twophase.pl lists pg_subtrans/
// with them).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type builtinSRFFunc func(ctx *Context, args []Datum) ([]Row, error)

var builtinSRFFuncs = map[string]builtinSRFFunc{
	"pg_ls_dir":    pgLsDir,
	"pg_stat_file": pgStatFile,

	"pg_logical_slot_get_binary_changes":  pgLogicalSlotGetBinaryChanges,
	"pg_logical_slot_peek_binary_changes": pgLogicalSlotPeekBinaryChanges,
}

// builtinSRFNotStrict names the functions whose NULL arguments are
// meaningful (proisstrict = false): the slot-changes functions take NULL
// upto_lsn / upto_nchanges for "no limit".
var builtinSRFNotStrict = map[string]bool{
	"pg_logical_slot_get_binary_changes":  true,
	"pg_logical_slot_peek_binary_changes": true,
}

type builtinSRFScanOp struct {
	plan *optimizer.BuiltinSRFScan
	rows []Row
	idx  int
}

func newBuiltinSRFScanOp(p *optimizer.BuiltinSRFScan) *builtinSRFScanOp {
	return &builtinSRFScanOp{plan: p}
}

func (o *builtinSRFScanOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *builtinSRFScanOp) Open(ctx *Context) error {
	o.rows, o.idx = nil, 0
	fn, ok := builtinSRFFuncs[o.plan.Func]
	if !ok {
		return &ExecError{Code: "0A000", Message: fmt.Sprintf("table-valued function %q not supported", o.plan.Func)}
	}
	// Correlated arguments see the current lateral outer row, as in
	// fromRegexpSplitToTableOp.Open.
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}
	args := make([]Datum, len(o.plan.Args))
	for i, e := range o.plan.Args {
		d, err := evalExpr(e, outerRow, ctx)
		if err != nil {
			return err
		}
		if d.IsNull() && !builtinSRFNotStrict[o.plan.Func] {
			return nil // every genfile.c function is strict
		}
		args[i] = d
	}
	rows, err := fn(ctx, args)
	if err != nil {
		return err
	}
	o.rows = rows
	return nil
}

func (o *builtinSRFScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

func (o *builtinSRFScanOp) Close() error { return nil }

// genfileSessionIsSuperuser approximates has_privs_of_role(GetUserId(),
// ROLE_PG_READ_SERVER_FILES) by the superuser case: goopg keeps an empty
// NonSuperuserRole for a superuser session.
func genfileSessionIsSuperuser(ctx *Context) bool {
	return ctx == nil || (ctx.NonSuperuserRole == "" && ctx.SecurityDefinerUser == "")
}

// convertAndCheckFilename is genfile.c convert_and_check_filename: the path
// canonicalised, then — unless the role may read any server file — refused
// when absolute outside the data directory or when relative but escaping
// it. It returns the canonical name (for messages) and the path to open,
// relative names resolving against the data directory (the backend's cwd).
func convertAndCheckFilename(ctx *Context, name string) (string, string, error) {
	canon := canonicalizePath(name)
	if !genfileSessionIsSuperuser(ctx) {
		if filepath.IsAbs(canon) {
			if ctx.DataDir == "" || !pathIsPrefixOfPath(canonicalizePath(ctx.DataDir), canon) {
				return "", "", &ExecError{Code: "42501", Message: "absolute path not allowed"}
			}
		} else if !pathIsRelativeAndBelowCwd(canon) {
			return "", "", &ExecError{Code: "42501", Message: "path must be in or below the data directory"}
		}
	}
	open := canon
	if !filepath.IsAbs(canon) && ctx != nil && ctx.DataDir != "" {
		open = filepath.Join(ctx.DataDir, canon)
	}
	return canon, open, nil
}

// canonicalizePath is port/path.c canonicalize_path for Unix paths: repeated
// and trailing separators and "." components dropped, ".." folded where a
// preceding component exists.
func canonicalizePath(p string) string {
	if p == "" {
		return p
	}
	c := path.Clean(p)
	if c == "." && p != "." {
		return "."
	}
	return c
}

// pathIsPrefixOfPath is path_is_prefix_of_path: path2 is path1 or below it.
func pathIsPrefixOfPath(path1, path2 string) bool {
	return path2 == path1 || strings.HasPrefix(path2, strings.TrimSuffix(path1, "/")+"/")
}

// pathIsRelativeAndBelowCwd is path_is_relative_and_below_cwd: not
// absolute, and not "..", "../…" after canonicalisation.
func pathIsRelativeAndBelowCwd(p string) bool {
	if filepath.IsAbs(p) {
		return false
	}
	return p != ".." && !strings.HasPrefix(p, "../")
}

// fileAccessError is errcode_for_file_access + "%m" for a failed file
// operation on name.
func fileAccessError(verb, name string, err error) error {
	code := "58030"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = "58P01"
	case errors.Is(err, fs.ErrPermission):
		code = "42501"
	}
	msg := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	if len(msg) > 0 {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return &ExecError{Code: code, Message: fmt.Sprintf("%s \"%s\": %s", verb, name, msg)}
}

// pgLsDir is genfile.c pg_ls_dir: the entries of a directory in readdir
// order, without "." and ".." unless include_dot_dirs; an absent directory
// yields no rows when missing_ok.
func pgLsDir(ctx *Context, args []Datum) ([]Row, error) {
	canon, dir, err := convertAndCheckFilename(ctx, args[0].StringValue())
	if err != nil {
		return nil, err
	}
	missingOK := len(args) > 1 && args[1].BoolValue()
	includeDot := len(args) > 2 && args[2].BoolValue()
	f, err := os.Open(dir)
	if err != nil {
		if missingOK && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fileAccessError("could not open directory", canon, err)
	}
	defer f.Close()
	// Readdirnames does not sort: readdir order, as ReadDir() in PG.
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, fileAccessError("could not read directory", canon, err)
	}
	var rows []Row
	if includeDot {
		rows = append(rows, Row{NewStringDatum(".")}, Row{NewStringDatum("..")})
	}
	for _, n := range names {
		rows = append(rows, Row{NewStringDatum(n)})
	}
	return rows, nil
}

// pgStatFile is genfile.c pg_stat_file: (size, access, modification,
// change, creation, isdir) from stat(2), times truncated to seconds
// (time_t_to_timestamptz). On Unix, change is st_ctime and creation NULL.
// A missing file with missing_ok returns the NULL record — one all-NULL
// row in FROM.
func pgStatFile(ctx *Context, args []Datum) ([]Row, error) {
	canon, name, err := convertAndCheckFilename(ctx, args[0].StringValue())
	if err != nil {
		return nil, err
	}
	missingOK := len(args) > 1 && args[1].BoolValue()
	fi, err := os.Stat(name)
	if err != nil {
		if missingOK && errors.Is(err, fs.ErrNotExist) {
			return []Row{{NullDatum, NullDatum, NullDatum, NullDatum, NullDatum, NullDatum}}, nil
		}
		return nil, fileAccessError("could not stat file", canon, err)
	}
	mtime := fi.ModTime().Truncate(time.Second)
	atime, ctime := mtime, mtime
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		atime = time.Unix(st.Atim.Sec, 0)
		ctime = time.Unix(st.Ctim.Sec, 0)
	}
	return []Row{{
		NewIntDatum(fi.Size()),
		NewTimestampTZDatum(atime),
		NewTimestampTZDatum(time.Unix(mtime.Unix(), 0)),
		NewTimestampTZDatum(ctime),
		NullDatum,
		NewBoolDatum(fi.IsDir()),
	}}, nil
}

func pgLogicalSlotGetBinaryChanges(ctx *Context, args []Datum) ([]Row, error) {
	return pgLogicalSlotBinaryChanges(ctx, args, false)
}

func pgLogicalSlotPeekBinaryChanges(ctx *Context, args []Datum) ([]Row, error) {
	return pgLogicalSlotBinaryChanges(ctx, args, true)
}

// pgLogicalSlotBinaryChanges is pg_logical_slot_get_changes_guts with
// binary output: (slot_name, upto_lsn, upto_nchanges, VARIADIC options).
// The slot name is required (22004 "slot name must not be null", as
// pg_logical_slot_get_changes_guts reports); upto_lsn / upto_nchanges are
// accepted but only NULL (decode to the current end of WAL) is honoured.
func pgLogicalSlotBinaryChanges(ctx *Context, args []Datum, peek bool) ([]Row, error) {
	if len(args) < 3 {
		return nil, &ExecError{Code: "42883", Message: "function requires slot_name, upto_lsn and upto_nchanges"}
	}
	if args[0].IsNull() {
		return nil, &ExecError{Code: "22004", Message: "slot name must not be null"}
	}
	if ctx.LogicalSlotChanges == nil {
		return nil, &ExecError{Code: "0A000", Message: "logical decoding is not available in this context"}
	}
	var options []string
	for _, a := range args[3:] {
		if a.IsNull() {
			return nil, &ExecError{Code: "22004", Message: "array must not contain nulls"}
		}
		options = append(options, a.StringValue())
	}
	changes, err := ctx.LogicalSlotChanges(args[0].StringValue(), peek, options,
		logicalMessageDBOid(ctx), catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(changes))
	for _, c := range changes {
		rows = append(rows, Row{NewStringDatum(formatPgLSN(c.LSN)), NewIntDatum(int64(c.XID)), NewBytesDatum(c.Data)})
	}
	return rows, nil
}

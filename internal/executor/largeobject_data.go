package executor

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Large-object data (M0149-0001h1a slice 2): pg_largeobject pages and the
// descriptor API of libpq/be-fsstubs.c over storage/large_object/inv_api.c.
//
// pg_largeobject (2613: loid oid, pageno int4, data bytea) holds an object's
// bytes in LOBLKSIZE (BLCKSZ/4 = 2048) pages. A page may be shorter than
// LOBLKSIZE and pages may be missing: both read back as zeros, and the
// object's size is the end of its last page (inv_getsize). Like the metadata
// rows, pages are written with writeHeapRowCanonical (xmin = writer XID, the
// pg_largeobject_loid_pn_index 2683 entry) and read / deleted through
// internal SQL, so they follow MVCC.

const (
	loBlkSize       = 2048                     // LOBLKSIZE
	loMaxObjectSize = int64(0x7fffffff) * 2048 // MAX_LARGE_OBJECT_SIZE
	invWrite        = 0x20000                  // INV_WRITE
	invRead         = 0x40000                  // INV_READ
)

// loDesc is a LargeObjectDesc: an open object, its offset and whether it was
// opened for writing (IFS_WRLOCK; every descriptor can read, IFS_RDLOCK).
type loDesc struct {
	loid   uint32
	offset int64
	write  bool
}

// loFDTable is one connection's descriptor array (be-fsstubs.c cookies[]).
// AtEOXact_LargeObject closes every descriptor at commit or abort; goopg does
// it from ReleaseAdvisoryTransactionLocks, which every transaction-end path
// (autocommit, COMMIT / ROLLBACK, chained, extended protocol) already calls
// with this same connection identity. (transam.Transaction.Handle is
// procNum+1, stable per backend, so it cannot tell transactions apart.)
type loFDTable struct {
	descs []*loDesc
}

var (
	loFDMu     sync.Mutex
	loFDTables = map[any]*loFDTable{}
)

// loSessionKey is the connection identity the transaction-end hook uses.
func loSessionKey(ctx *Context) any {
	if ctx.AdvisorySessionIdentity != nil {
		return ctx.AdvisorySessionIdentity
	}
	if ctx.Session != nil {
		return ctx.Session
	}
	return ctx
}

// loFDs returns the session's descriptor table. Callers hold loFDMu.
func loFDs(ctx *Context) *loFDTable {
	k := loSessionKey(ctx)
	t := loFDTables[k]
	if t == nil {
		t = &loFDTable{}
		loFDTables[k] = t
	}
	return t
}

// ReleaseLargeObjectDescriptors is AtEOXact_LargeObject: it drops a
// connection's descriptors at transaction end and at session end.
func ReleaseLargeObjectDescriptors(identity any) {
	if identity == nil {
		return
	}
	loFDMu.Lock()
	delete(loFDTables, identity)
	loFDMu.Unlock()
}

// loDescFor resolves fd (42704 "invalid large-object descriptor").
func loDescFor(ctx *Context, fd int64) (*loDesc, error) {
	loFDMu.Lock()
	defer loFDMu.Unlock()
	t := loFDs(ctx)
	if fd < 0 || fd >= int64(len(t.descs)) || t.descs[fd] == nil {
		return nil, &ExecError{Code: "42704", Message: fmt.Sprintf("invalid large-object descriptor: %d", fd)}
	}
	return t.descs[fd], nil
}

// loCloseObjectFDs closes every descriptor on loid (be_lo_unlink).
func loCloseObjectFDs(ctx *Context, loid uint32) {
	loFDMu.Lock()
	defer loFDMu.Unlock()
	t := loFDs(ctx)
	for i, d := range t.descs {
		if d != nil && d.loid == loid {
			t.descs[i] = nil
		}
	}
}

// loPage is one pg_largeobject row.
type loPage struct {
	no   int32
	data []byte
}

func loDataTable(ctx *Context) (*catalog.Table, error) {
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_largeobject"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok || tbl == nil || tbl.Virtual {
		return nil, &ExecError{Code: "0A000", Message: "large objects are not supported in this database"}
	}
	return tbl, nil
}

// loPages returns loid's visible pages numbered from..to (to < 0: no upper
// bound), in page order.
func loPages(ctx *Context, loid uint32, from, to int32) ([]loPage, error) {
	q := fmt.Sprintf("SELECT pageno, data FROM pg_catalog.pg_largeobject WHERE loid = %d::oid AND pageno >= %d", loid, from)
	if to >= 0 {
		q += fmt.Sprintf(" AND pageno <= %d", to)
	}
	rows, err := internalCatalogQuery(ctx, q+" ORDER BY pageno")
	if err != nil {
		return nil, err
	}
	out := make([]loPage, 0, len(rows))
	for _, r := range rows {
		b := r[1].BytesValue()
		out = append(out, loPage{no: int32(r[0].Int), data: append([]byte(nil), b...)})
	}
	return out, nil
}

// loLastPage is the visible page with the highest number (ok=false: none).
func loLastPage(ctx *Context, loid uint32) (loPage, bool, error) {
	rows, err := internalCatalogQuery(ctx, fmt.Sprintf(
		"SELECT pageno, data FROM pg_catalog.pg_largeobject WHERE loid = %d::oid ORDER BY pageno DESC LIMIT 1", loid))
	if err != nil || len(rows) == 0 {
		return loPage{}, false, err
	}
	return loPage{no: int32(rows[0][0].Int), data: append([]byte(nil), rows[0][1].BytesValue()...)}, true, nil
}

// loSize is inv_getsize: the end of the last page.
func loSize(ctx *Context, loid uint32) (int64, error) {
	p, ok, err := loLastPage(ctx, loid)
	if err != nil || !ok {
		return 0, err
	}
	return int64(p.no)*loBlkSize + int64(len(p.data)), nil
}

// loDeletePages deletes loid's pages numbered from..to (to < 0: all after).
func loDeletePages(ctx *Context, loid uint32, from, to int32) error {
	q := fmt.Sprintf("DELETE FROM pg_catalog.pg_largeobject WHERE loid = %d::oid AND pageno >= %d", loid, from)
	if to >= 0 {
		q += fmt.Sprintf(" AND pageno <= %d", to)
	}
	_, err := internalCatalogQuery(ctx, q)
	return err
}

// loInsertPage writes one pg_largeobject row.
func loInsertPage(ctx *Context, loid uint32, pageno int32, data []byte) error {
	tbl, err := loDataTable(ctx)
	if err != nil {
		return err
	}
	row := Row{NewIntDatum(int64(loid)), NewIntDatum(int64(pageno)), NewBytesDatum(append([]byte(nil), data...))}
	_, err = writeHeapRowCanonical(ctx, ctx.Catalog.RelFileNode(tbl), tbl.Columns, row)
	return err
}

// loReplacePage is CatalogTupleUpdate of one page: the old version (if any)
// is deleted and the new one inserted.
func loReplacePage(ctx *Context, loid uint32, pageno int32, existed bool, data []byte) error {
	if existed {
		if err := loDeletePages(ctx, loid, pageno, pageno); err != nil {
			return err
		}
	}
	return loInsertPage(ctx, loid, pageno, data)
}

// loRead is inv_read: up to n bytes from off, holes as zeros, stopping at
// the end of the last page.
func loRead(ctx *Context, loid uint32, off int64, n int64) ([]byte, error) {
	if n <= 0 {
		return []byte{}, nil
	}
	first := int32(off / loBlkSize)
	last := int64(first) + (n+loBlkSize-1)/loBlkSize + 1
	if last > 0x7fffffff {
		last = 0x7fffffff
	}
	pages, err := loPages(ctx, loid, first, int32(last))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, n)
	pos := off
	for _, p := range pages {
		if int64(len(out)) >= n {
			break
		}
		pageOff := int64(p.no) * loBlkSize
		if pageOff > pos { // a hole before this page reads as zeros
			gap := pageOff - pos
			if gap > n-int64(len(out)) {
				gap = n - int64(len(out))
			}
			out = append(out, make([]byte, gap)...)
			pos += gap
			if int64(len(out)) >= n {
				break
			}
		}
		end := pageOff + int64(len(p.data))
		if pos >= end {
			continue
		}
		take := end - pos
		if take > n-int64(len(out)) {
			take = n - int64(len(out))
		}
		out = append(out, p.data[pos-pageOff:pos-pageOff+take]...)
		pos += take
	}
	return out, nil
}

// loWrite is inv_write: buf at off, page by page; an existing page keeps the
// bytes outside the written range and grows to cover it, a new page is
// zero-filled before the written bytes.
func loWrite(ctx *Context, loid uint32, off int64, buf []byte) error {
	if off+int64(len(buf)) > loMaxObjectSize {
		return &ExecError{Code: "22023", Message: fmt.Sprintf("invalid large object write request size: %d", len(buf))}
	}
	if len(buf) == 0 {
		return nil
	}
	first := int32(off / loBlkSize)
	lastNo := int32((off + int64(len(buf)) - 1) / loBlkSize)
	existing, err := loPages(ctx, loid, first, lastNo)
	if err != nil {
		return err
	}
	have := map[int32][]byte{}
	for _, p := range existing {
		have[p.no] = p.data
	}
	pos, done := off, 0
	for done < len(buf) {
		pageno := int32(pos / loBlkSize)
		in := int(pos % loBlkSize)
		n := loBlkSize - in
		if n > len(buf)-done {
			n = len(buf) - done
		}
		old, existed := have[pageno]
		size := in + n
		if len(old) > size {
			size = len(old)
		}
		page := make([]byte, size)
		copy(page, old)
		copy(page[in:], buf[done:done+n])
		if err := loReplacePage(ctx, loid, pageno, existed, page); err != nil {
			return err
		}
		pos += int64(n)
		done += n
	}
	return nil
}

// loTruncate is inv_truncate. If the page holding len exists it keeps
// len%LOBLKSIZE bytes (zero-extended); otherwise the first later page, if
// any, is deleted and a page of len%LOBLKSIZE zero bytes is inserted — even
// an empty one. Every page after the truncation point is then deleted.
func loTruncate(ctx *Context, loid uint32, length int64) error {
	if length < 0 || length > loMaxObjectSize {
		return &ExecError{Code: "22023", Message: fmt.Sprintf("invalid large object truncation target: %d", length)}
	}
	pageno := int32(length / loBlkSize)
	off := int(length % loBlkSize)
	pages, err := loPages(ctx, loid, pageno, -1)
	if err != nil {
		return err
	}
	if len(pages) > 0 && pages[0].no == pageno {
		page := make([]byte, off)
		copy(page, pages[0].data)
		if err := loReplacePage(ctx, loid, pageno, true, page); err != nil {
			return err
		}
	} else {
		if len(pages) > 0 {
			if err := loDeletePages(ctx, loid, pages[0].no, pages[0].no); err != nil {
				return err
			}
		}
		if err := loInsertPage(ctx, loid, pageno, make([]byte, off)); err != nil {
			return err
		}
	}
	if len(pages) > 0 {
		return loDeletePages(ctx, loid, pageno+1, -1)
	}
	return nil
}

// loOpenCheck is inv_open's existence and permission checks; write asks for
// UPDATE as well as SELECT.
func loOpenCheck(ctx *Context, loid uint32, write bool) error {
	owner, acl, found, err := loLookup(ctx, loid)
	if err != nil {
		return err
	}
	if !found {
		return &ExecError{Code: "42704", Message: fmt.Sprintf("large object %d does not exist", loid)}
	}
	if loCompatPrivileges(ctx) {
		return nil
	}
	im, err := privilegeCatalog("lo_open", ctx, 0)
	if err != nil {
		return err
	}
	role := currentRoleOID(im, ctx)
	obj := aclObject{owner: owner, aclText: acl, letters: "rw", defaultOwner: "rw"}
	if !aclAnyHeld(im, role, obj, []privWant{{letter: 'r'}}) ||
		(write && !aclAnyHeld(im, role, obj, []privWant{{letter: 'w'}})) {
		return &ExecError{Code: "42501", Message: fmt.Sprintf("permission denied for large object %d", loid)}
	}
	return nil
}

// loIntArg reads an integer argument.
func loIntArg(d Datum) int64 { return d.Int }

// evalLargeObjectFunc dispatches the descriptor and bytea functions of
// be-fsstubs.c. Every one is strict.
func evalLargeObjectFunc(name string, args []Datum, ctx *Context) (Datum, error) {
	if anyNull(args) {
		return NullDatum, nil
	}
	switch name {
	case "lo_open":
		loid, mode := uint32(loIntArg(args[0])), loIntArg(args[1])
		write, read := mode&invWrite != 0, mode&invRead != 0
		if write {
			if err := loPreventReadOnly(ctx, "lo_open(INV_WRITE)"); err != nil {
				return Datum{}, err
			}
		}
		if !write && !read {
			return Datum{}, &ExecError{Code: "22023", Message: fmt.Sprintf("invalid flags for opening a large object: %d", int32(mode))}
		}
		if err := loOpenCheck(ctx, loid, write); err != nil {
			return Datum{}, err
		}
		loFDMu.Lock()
		t := loFDs(ctx)
		fd := -1
		for i, d := range t.descs {
			if d == nil {
				fd = i
				break
			}
		}
		if fd < 0 {
			fd = len(t.descs)
			t.descs = append(t.descs, nil)
		}
		t.descs[fd] = &loDesc{loid: loid, write: write}
		loFDMu.Unlock()
		return NewIntDatum(int64(fd)), nil
	case "lo_close":
		fd := loIntArg(args[0])
		if _, err := loDescFor(ctx, fd); err != nil {
			return Datum{}, err
		}
		loFDMu.Lock()
		loFDs(ctx).descs[fd] = nil
		loFDMu.Unlock()
		return NewIntDatum(0), nil
	case "loread":
		d, err := loDescFor(ctx, loIntArg(args[0]))
		if err != nil {
			return Datum{}, err
		}
		n := loIntArg(args[1])
		if n < 0 {
			n = 0
		}
		b, err := loRead(ctx, d.loid, d.offset, n)
		if err != nil {
			return Datum{}, err
		}
		d.offset += int64(len(b))
		return NewBytesDatum(b), nil
	case "lowrite":
		if err := loPreventReadOnly(ctx, "lowrite()"); err != nil {
			return Datum{}, err
		}
		fd := loIntArg(args[0])
		d, err := loDescFor(ctx, fd)
		if err != nil {
			return Datum{}, err
		}
		if !d.write {
			return Datum{}, &ExecError{Code: "55000", Message: fmt.Sprintf("large object descriptor %d was not opened for writing", fd)}
		}
		buf := args[1].BytesValue()
		if err := loWrite(ctx, d.loid, d.offset, buf); err != nil {
			return Datum{}, err
		}
		d.offset += int64(len(buf))
		return NewIntDatum(int64(len(buf))), nil
	case "lo_lseek", "lo_lseek64":
		fd := loIntArg(args[0])
		d, err := loDescFor(ctx, fd)
		if err != nil {
			return Datum{}, err
		}
		off, whence := loIntArg(args[1]), loIntArg(args[2])
		var target int64
		switch whence {
		case 0:
			target = off
		case 1:
			target = d.offset + off
		case 2:
			size, err := loSize(ctx, d.loid)
			if err != nil {
				return Datum{}, err
			}
			target = size + off
		default:
			return Datum{}, &ExecError{Code: "22023", Message: fmt.Sprintf("invalid whence setting: %d", whence)}
		}
		if target < 0 || target > loMaxObjectSize {
			return Datum{}, &ExecError{Code: "22023", Message: fmt.Sprintf("invalid large object seek target: %d", target)}
		}
		d.offset = target
		if name == "lo_lseek" && target > 0x7fffffff {
			return Datum{}, &ExecError{Code: "22003", Message: fmt.Sprintf("lo_lseek result out of range for large-object descriptor %d", fd)}
		}
		return NewIntDatum(target), nil
	case "lo_tell", "lo_tell64":
		fd := loIntArg(args[0])
		d, err := loDescFor(ctx, fd)
		if err != nil {
			return Datum{}, err
		}
		if name == "lo_tell" && d.offset > 0x7fffffff {
			return Datum{}, &ExecError{Code: "22003", Message: fmt.Sprintf("lo_tell result out of range for large-object descriptor %d", fd)}
		}
		return NewIntDatum(d.offset), nil
	case "lo_truncate", "lo_truncate64":
		if err := loPreventReadOnly(ctx, name+"()"); err != nil {
			return Datum{}, err
		}
		fd := loIntArg(args[0])
		d, err := loDescFor(ctx, fd)
		if err != nil {
			return Datum{}, err
		}
		if !d.write {
			return Datum{}, &ExecError{Code: "55000", Message: fmt.Sprintf("large object descriptor %d was not opened for writing", fd)}
		}
		if err := loTruncate(ctx, d.loid, loIntArg(args[1])); err != nil {
			return Datum{}, err
		}
		return NewIntDatum(0), nil
	case "lo_get":
		loid := uint32(loIntArg(args[0]))
		if err := loOpenCheck(ctx, loid, false); err != nil {
			return Datum{}, err
		}
		size, err := loSize(ctx, loid)
		if err != nil {
			return Datum{}, err
		}
		off, n := int64(0), int64(-1)
		if len(args) == 3 {
			off, n = loIntArg(args[1]), loIntArg(args[2])
			if n < 0 {
				return Datum{}, &ExecError{Code: "22023", Message: "requested length cannot be negative"}
			}
		}
		length := int64(0)
		if size > off {
			length = size - off
			if n >= 0 && n <= length {
				length = n
			}
		}
		b, err := loRead(ctx, loid, off, length)
		if err != nil {
			return Datum{}, err
		}
		return NewBytesDatum(b), nil
	case "lo_put":
		if err := loPreventReadOnly(ctx, "lo_put()"); err != nil {
			return Datum{}, err
		}
		loid := uint32(loIntArg(args[0]))
		if err := loOpenCheck(ctx, loid, true); err != nil {
			return Datum{}, err
		}
		if err := loWrite(ctx, loid, loIntArg(args[1]), args[2].BytesValue()); err != nil {
			return Datum{}, err
		}
		return NullDatum, nil
	case "lo_from_bytea":
		if err := loPreventReadOnly(ctx, "lo_from_bytea()"); err != nil {
			return Datum{}, err
		}
		d, err := evalLoCreate("lo_create", args[:1], ctx)
		if err != nil {
			return Datum{}, err
		}
		if err := loWrite(ctx, uint32(d.Int), 0, args[1].BytesValue()); err != nil {
			return Datum{}, err
		}
		return d, nil
	case "lo_import":
		return evalLoImport(args, ctx)
	case "lo_export":
		return evalLoExport(args, ctx)
	}
	return Datum{}, &ExecError{Code: "42883", Message: fmt.Sprintf("function %s does not exist", name)}
}

// loServerFileCheck is the EXECUTE check PG gets from system_functions.sql's
// REVOKE EXECUTE ON FUNCTION lo_import / lo_export FROM public: only a
// superuser may call them.
func loServerFileCheck(ctx *Context, fn string) error {
	im, err := privilegeCatalog(fn, ctx, 0)
	if err != nil {
		return err
	}
	if !im.IsSuperuser(currentRoleOID(im, ctx)) {
		return &ExecError{Code: "42501", Message: "permission denied for function " + fn}
	}
	return nil
}

// evalLoImport is be_lo_import / be_lo_import_with_oid: a server file
// becomes a new large object.
func evalLoImport(args []Datum, ctx *Context) (Datum, error) {
	if err := loServerFileCheck(ctx, "lo_import"); err != nil {
		return Datum{}, err
	}
	if err := loPreventReadOnly(ctx, "lo_import()"); err != nil {
		return Datum{}, err
	}
	path := args[0].StringValue()
	data, err := os.ReadFile(path)
	if err != nil {
		return Datum{}, loFileError("could not open server file \"%s\": %s", path, err)
	}
	oidArg := []Datum{NewIntDatum(0)}
	if len(args) == 2 {
		oidArg = args[1:2]
	}
	d, err := evalLoCreate("lo_create", oidArg, ctx)
	if err != nil {
		return Datum{}, err
	}
	if err := loWrite(ctx, uint32(d.Int), 0, data); err != nil {
		return Datum{}, err
	}
	return d, nil
}

// evalLoExport is be_lo_export: the object's bytes are written to a new
// server file (mode 0644 under the server's umask).
func evalLoExport(args []Datum, ctx *Context) (Datum, error) {
	if err := loServerFileCheck(ctx, "lo_export"); err != nil {
		return Datum{}, err
	}
	loid := uint32(loIntArg(args[0]))
	if err := loOpenCheck(ctx, loid, false); err != nil {
		return Datum{}, err
	}
	size, err := loSize(ctx, loid)
	if err != nil {
		return Datum{}, err
	}
	data, err := loRead(ctx, loid, 0, size)
	if err != nil {
		return Datum{}, err
	}
	path := args[1].StringValue()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Datum{}, loFileError("could not create server file \"%s\": %s", path, err)
	}
	return NewIntDatum(1), nil
}

// loFileError maps an OS error to errcode_for_file_access's SQLSTATE and
// strerror text.
func loFileError(format, path string, err error) error {
	code := "58030"
	msg := err.Error()
	if pe, ok := err.(*os.PathError); ok {
		msg = pe.Err.Error()
	}
	switch {
	case os.IsNotExist(err):
		code, msg = "58P01", "No such file or directory"
	case os.IsPermission(err):
		code, msg = "42501", "Permission denied"
	default:
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
	}
	return &ExecError{Code: code, Message: fmt.Sprintf(format, path, msg)}
}

// isLargeObjectDataFunc reports the be-fsstubs.c functions above.
func isLargeObjectDataFunc(name string) bool {
	switch name {
	case "lo_open", "lo_close", "loread", "lowrite", "lo_lseek", "lo_lseek64",
		"lo_tell", "lo_tell64", "lo_truncate", "lo_truncate64", "lo_get",
		"lo_put", "lo_from_bytea", "lo_import", "lo_export":
		return true
	}
	return false
}

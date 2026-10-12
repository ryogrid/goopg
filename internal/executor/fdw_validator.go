package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// postgresqlFDWValidatorOID is pg_proc's postgresql_fdw_validator(text[],
// oid), the one validator PostgreSQL ships (foreign.c). It is not a user
// routine, so resolveFDWValidatorFunc falls back to it by name.
const postgresqlFDWValidatorOID = 2316

// The catalog whose options a validator is checking — the second argument
// PostgreSQL passes to the validator (transformGenericOptions' catalogId).
const (
	fdwOptionsCatalogWrapper     = 2328 // ForeignDataWrapperRelationId
	fdwOptionsCatalogServer      = 1417 // ForeignServerRelationId
	fdwOptionsCatalogUserMapping = 1418 // UserMappingRelationId
	fdwOptionsCatalogForeignTbl  = 3118 // ForeignTableRelationId
	fdwOptionsCatalogAttribute   = 1249 // AttributeRelationId
)

// libpqConninfoOptions is foreign.c's libpq_conninfo_options: the options
// postgresql_fdw_validator accepts, each in one catalog (user and password
// only on a user mapping, the rest only on a server).
var libpqConninfoOptions = []struct {
	name    string
	catalog uint32
}{
	{"authtype", fdwOptionsCatalogServer},
	{"service", fdwOptionsCatalogServer},
	{"user", fdwOptionsCatalogUserMapping},
	{"password", fdwOptionsCatalogUserMapping},
	{"connect_timeout", fdwOptionsCatalogServer},
	{"dbname", fdwOptionsCatalogServer},
	{"host", fdwOptionsCatalogServer},
	{"hostaddr", fdwOptionsCatalogServer},
	{"port", fdwOptionsCatalogServer},
	{"tty", fdwOptionsCatalogServer},
	{"options", fdwOptionsCatalogServer},
	{"requiressl", fdwOptionsCatalogServer},
	{"sslmode", fdwOptionsCatalogServer},
	{"gsslib", fdwOptionsCatalogServer},
	{"gssdelegation", fdwOptionsCatalogServer},
}

// builtinFDWValidatorOID resolves a validator name to a built-in validator:
// postgresql_fdw_validator, unqualified or pg_catalog-qualified.
func builtinFDWValidatorOID(name parser.ObjectName) (uint32, bool) {
	if (name.Schema == "" || strings.EqualFold(name.Schema, "pg_catalog")) &&
		strings.EqualFold(name.Name, "postgresql_fdw_validator") {
		return postgresqlFDWValidatorOID, true
	}
	return 0, false
}

// validateFDWOptions runs a wrapper's validator over the options destined for
// catalogOID, as transformGenericOptions does after merging an OPTIONS clause.
// Only the built-in postgresql_fdw_validator is evaluated; a user-defined
// validator function is recorded but not invoked (see the M0122-0015g ledger
// row). options are "name=value" elements.
func validateFDWOptions(validatorOID uint32, options []string, catalogOID uint32) *ExecError {
	if validatorOID != postgresqlFDWValidatorOID {
		return nil
	}
	for _, o := range options {
		name := o
		if i := strings.IndexByte(o, '='); i >= 0 {
			name = o[:i]
		}
		if isConninfoOption(name, catalogOID) {
			continue
		}
		// postgresql_fdw_validator: complain, hinting at a similar option
		// valid in this catalog (or that there are none).
		var candidates []string
		for _, opt := range libpqConninfoOptions {
			if opt.catalog == catalogOID {
				candidates = append(candidates, opt.name)
			}
		}
		e := &ExecError{Code: "42601", Message: fmt.Sprintf("invalid option %q", name)}
		switch {
		case len(candidates) == 0:
			e.Hint = "There are no valid options in this context."
		default:
			if m := closestMatch(name, candidates, 4); m != "" {
				e.Hint = fmt.Sprintf("Perhaps you meant the option %q.", m)
			}
		}
		return e
	}
	return nil
}

// isConninfoOption is foreign.c's is_conninfo_option.
func isConninfoOption(name string, catalogOID uint32) bool {
	for _, opt := range libpqConninfoOptions {
		if opt.catalog == catalogOID && opt.name == name {
			return true
		}
	}
	return false
}

// closestMatch is varlena.c's initClosestMatch / updateClosestMatch /
// getClosestMatch: the first candidate at the smallest Levenshtein distance
// that is at most maxD and at most half the source's length, or "".
func closestMatch(source string, candidates []string, maxD int) string {
	if source == "" {
		return ""
	}
	best, bestD := "", -1
	half := len(source) / 2 // strlen: bytes
	for _, c := range candidates {
		if c == "" {
			continue
		}
		d := levenshtein(source, c)
		if d <= maxD && d <= half && (bestD == -1 || d < bestD) {
			best, bestD = c, d
		}
	}
	return best
}

// levenshtein is the unit-cost edit distance by character, as
// varstr_levenshtein computes it.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			sub := prev[j-1]
			if ra[i-1] != rb[j-1] {
				sub++
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// fdwValidatorForServer returns the validator of the wrapper the named server
// uses, in dbOid's database (0 when the server or wrapper is unknown).
func fdwValidatorForServer(im *catalog.InMemory, server string, dbOid uint32) uint32 {
	srv, ok := im.LookupForeignServer(server, dbOid)
	if !ok || srv == nil {
		return 0
	}
	fdw, ok := im.LookupForeignDataWrapper(srv.FdwName, srv.DBOid)
	if !ok || fdw == nil {
		return 0
	}
	return fdw.ValidatorOID
}

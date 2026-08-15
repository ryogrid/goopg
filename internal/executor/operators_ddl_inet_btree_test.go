package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0134-0002 C5: inet/cidr B-tree keys. The encoder's byte order must equal
// network_cmp_internal's total order (postgres/src/backend/utils/adt/network.c:
// 402-420) under a pure byte-wise comparison, because the btree compares
// encoded keys directly — there is no operator call to fall back on. The
// reference comparator below is a direct port of network_cmp_internal +
// bitncmp (network.c:1533-1560), fed with the same parsed values the encoder
// sees.

// inetRefVal is a parsed inet/cidr value for the test-side network_cmp port.
type inetRefVal struct {
	family int
	addr   []byte
	bits   int
}

func parseInetRef(t *testing.T, lit string, isCidr bool) inetRefVal {
	t.Helper()
	family, addr, bits, err := parseInetKeyText(lit, isCidr)
	if err != nil {
		t.Fatalf("parse %q: %v", lit, err)
	}
	return inetRefVal{int(family), addr, bits}
}

// inetBitncmp is a direct port of bitncmp: compare n bits MSB-first — whole
// bytes via memcmp, then the partial byte bit-by-bit from the top.
func inetBitncmp(l, r []byte, n int) int {
	b := n / 8
	if x := bytes.Compare(l[:b], r[:b]); x != 0 || n%8 == 0 {
		return x
	}
	lb, rb := l[b], r[b]
	for i := 0; i < n%8; i++ {
		lbSet := lb&0x80 != 0
		rbSet := rb&0x80 != 0
		if lbSet != rbSet {
			if lbSet {
				return 1
			}
			return -1
		}
		lb <<= 1
		rb <<= 1
	}
	return 0
}

// networkCmpRef is a direct port of network_cmp_internal: same-family compares
// the common prefix bits, then the netmask length ascending, then the full
// address; cross-family compares the family value (PGSQL_AF_INET 2 <
// PGSQL_AF_INET6 3).
func networkCmpRef(a, b inetRefVal) int {
	if a.family == b.family {
		minBits := a.bits
		if b.bits < minBits {
			minBits = b.bits
		}
		if order := inetBitncmp(a.addr, b.addr, minBits); order != 0 {
			return order
		}
		if order := a.bits - b.bits; order != 0 {
			return order
		}
		maxBits := 32
		if a.family != 2 {
			maxBits = 128
		}
		return inetBitncmp(a.addr, b.addr, maxBits)
	}
	return a.family - b.family
}

func cmpSign(x int) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}

// inetKeyOrderCorpus exercises the ordering-sensitive shapes: /0 edge, partial
// bytes (masks that are not byte-aligned), equal-prefix-different-mask, equal
// mask with host-bit differences, unequal prefixes with different masks, and
// the v4-vs-v6 cross-family boundary. "::1/128" and "::1" parse to the same
// value (inet/cidr default to /128), pinning the equal-encoded-bytes branch.
var inetKeyOrderCorpus = []string{
	"0.0.0.0/0",
	"0.0.0.0/8",
	"0.0.0.0/32",
	"1.0.0.0/8",
	"10.0.0.0/8",
	"10.0.0.1/8", // equal /8 prefix with 10.0.0.0/8, host-bit tiebreak
	"10.0.0.0/9", // partial-byte mask (9)
	"10.0.0.0/16",
	"10.0.0.1/16",
	"10.0.0.0/24",
	"10.0.0.1/24",
	"10.0.0.0/25", // partial-byte mask (25)
	"10.0.0.0/32",
	"10.0.0.1/32",
	"10.0.0.2/32",
	"10.0.255.0/16",
	"10.1.0.0/16",
	"10.128.0.0/9",
	"11.0.0.0/9", // unequal prefix AND mask vs 10.0.0.0/8
	"128.0.0.0/1",
	"192.168.1.0/24",
	"192.168.1.1/24",
	"255.255.255.255/32",
	"::1/128",
	"::1",
	"2001:db8::/32",
	"2001:db8::1/64",
	"2001:db8::1/128",
	"fe80::1/64",
	"fe80::1/128",
	"ff00::/8",
	"ffff::/8",
}

// TestSupportedBTreeKeyTypeAcceptsInet is the allow-list gate: CREATE TABLE /
// CREATE INDEX on inet and cidr columns must no longer hit
// btreeKeyTypeRejectionError, and the pre-existing scalar types must stay
// accepted (the corpus below would regress them if the allow-list shrank).
func TestSupportedBTreeKeyTypeAcceptsInet(t *testing.T) {
	for _, typ := range []string{"inet", "cidr", "int4", "int8", "numeric", "text", "int2", "bool", "bytea"} {
		if !isSupportedBTreeKeyType(typ) {
			t.Errorf("isSupportedBTreeKeyType(%q) = false, want true", typ)
		}
	}
	// Non-vacuity: types with no btree opclass in PG must stay rejected.
	for _, typ := range []string{"box", "point", "json", "jsonb"} {
		if isSupportedBTreeKeyType(typ) {
			t.Errorf("isSupportedBTreeKeyType(%q) = true, want false", typ)
		}
	}
}

// TestInetBTreeKeyByteOrderMatchesNetworkCmp proves the encoded key's byte-wise
// order equals PG network_cmp_internal's total order over a corpus, for both
// column types that share the btree/inet_ops key (inet and its binary-coercible
// sibling cidr). Without the masked-network component this fails: raw-address
// encoding would put 10.0.0.1/8 ABOVE 10.0.0.0/32 (host byte 1 vs 0) while PG
// sorts them by the equal 8-bit prefix then 8<32.
func TestInetBTreeKeyByteOrderMatchesNetworkCmp(t *testing.T) {
	for _, typ := range []string{"inet", "cidr"} {
		t.Run(typ, func(t *testing.T) {
			col := &catalog.Column{Name: "k", Type: catalog.Type{Name: typ}}
			enc := make([][]byte, len(inetKeyOrderCorpus))
			ref := make([]inetRefVal, len(inetKeyOrderCorpus))
			for i, lit := range inetKeyOrderCorpus {
				k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
				if err != nil {
					t.Fatalf("encode %q: %v", lit, err)
				}
				if len(k) == 0 {
					t.Fatalf("encode %q produced an empty key", lit)
				}
				enc[i] = k
				ref[i] = parseInetRef(t, lit, typ == "cidr")
			}
			for i := 0; i < len(enc); i++ {
				for j := i + 1; j < len(enc); j++ {
					got := bytes.Compare(enc[i], enc[j])
					want := networkCmpRef(ref[i], ref[j])
					if cmpSign(got) != cmpSign(want) {
						t.Errorf("%s vs %s: encoded bytes compare %d but network_cmp says %d",
							inetKeyOrderCorpus[i], inetKeyOrderCorpus[j], got, want)
					}
				}
			}
		})
	}
}

// TestInetBTreeKeyKnownOrderings pins the brief's load-bearing orderings as
// literal byte sequences, so a failure names the exact pair rather than a
// corpus sweep.
func TestInetBTreeKeyKnownOrderings(t *testing.T) {
	col := &catalog.Column{Name: "k", Type: catalog.Type{Name: "inet"}}
	encode := func(lit string) []byte {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", lit, err)
		}
		return k
	}
	sequences := [][]string{
		// Cross-family: PGSQL_AF_INET (2) < PGSQL_AF_INET6 (3).
		{"10.0.0.1/8", "::1/128"},
		// Equal-prefix-different-mask: 8-bit common prefix equal, then 8<32.
		{"10.0.0.1/8", "10.0.0.0/32"},
		// Different-mask-unequal-prefix: first 8 bits 10 vs 11 decide first.
		{"10.0.0.0/8", "11.0.0.0/9"},
		// Equal-net-equal-mask-different-host: full-address tiebreak.
		{"10.0.0.0/24", "10.0.0.1/24"},
		// inet default mask (/32) omitted from output, ordering unaffected.
		{"10.0.0.1", "10.0.0.2"},
	}
	for _, seq := range sequences {
		var prev []byte
		for i, lit := range seq {
			k := encode(lit)
			if i > 0 && bytes.Compare(prev, k) >= 0 {
				t.Errorf("expected %q < %q but encoded %x >= %x", seq[i-1], lit, prev, k)
			}
			prev = k
		}
	}
}

// TestInetBTreeKeyDecodeRoundTrips is the encode<->decode twin gate: the key
// decodes back to a text form that re-encodes to the SAME bytes. Host-bit
// cidr values (10.0.0.1/32) pin the cidr_out rule — cidr output must keep the
// /n suffix (network.c:155-159) or the classful re-parse would change /32 to
// /8. inet values pin the inet_out rule — the default /32 suffix is dropped,
// and the inet input default re-derives it.
func TestInetBTreeKeyDecodeRoundTrips(t *testing.T) {
	for _, typ := range []string{"inet", "cidr"} {
		t.Run(typ, func(t *testing.T) {
			col := &catalog.Column{Name: "k", Type: catalog.Type{Name: typ}}
			values := []string{
				"0.0.0.0/0", "10.0.0.1/8", "10.0.0.0/8", "10.0.0.1/32",
				"192.168.1.1/24", "255.255.255.255/32", "10.0.0.1",
				"::1/128", "::1", "2001:db8::1/64", "fe80::1", "::", "ffff::/8",
			}
			for _, lit := range values {
				key, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
				if err != nil {
					t.Fatalf("encode %q: %v", lit, err)
				}
				d, n, handled, derr := decodeScalarBTreeKey(key, typ)
				if derr != nil {
					t.Fatalf("decode %q: %v", lit, derr)
				}
				if !handled {
					t.Fatalf("decode %q: inet arm did not handle the key", lit)
				}
				if n != len(key) {
					t.Errorf("%s: decode consumed %d bytes, key is %d", lit, n, len(key))
				}
				if d.Kind != KindString {
					t.Errorf("%s: decoded kind %d, want KindString", lit, d.Kind)
				}
				re, rerr := encodeBTreeKeyForColumn(nil, d, col, 0)
				if rerr != nil {
					t.Fatalf("re-encode decoded %q: %v", lit, rerr)
				}
				if !bytes.Equal(re, key) {
					t.Errorf("%s: re-encoded %x != original %x (decoded text %q)",
						lit, re, key, d.StringValue())
				}
			}
		})
	}
}

// TestInetExpressionKeyGateAcceptsInet exercises the SECOND parallel gate in
// createBTreeIndex (the expression-key branch, operators_ddl.go): an index
// expression whose resolved type is inet must pass isSupportedBTreeKeyType the
// same way a named inet column does. Before C5 this expression would have been
// rejected with btreeKeyTypeRejectionError and the named-column fix alone
// would have left the two gates disagreeing.
func TestInetExpressionKeyGateAcceptsInet(t *testing.T) {
	tbl := &catalog.Table{
		Schema: "public",
		Name:   "t",
		Columns: []catalog.Column{
			{Name: "x", Type: catalog.Type{Name: "inet"}, Ordinal: 0},
		},
	}
	pe, err := parser.ParseExpr("x")
	if err != nil {
		t.Fatalf("parse expression: %v", err)
	}
	// createBTreeIndex resolves index expressions exactly like the build path:
	// ResolveIndexPredicate populates the ColumnRef's Type, which
	// ExprResultType then reports.
	planExpr, err := planner.ResolveIndexPredicate(pe, tbl)
	if err != nil || planExpr == nil {
		t.Fatalf("ResolveIndexPredicate: %v", err)
	}
	typ, ok := planner.ExprResultType(planExpr)
	if !ok {
		t.Fatalf("ExprResultType: inet column reference resolved to no type")
	}
	if !isSupportedBTreeKeyType(typ.Name) {
		t.Errorf("expression-key gate rejected resolved type %q", typ.Name)
	}
}

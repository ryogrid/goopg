package executor

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/catalog"
)

// bpcharWidthCase is driven through BOTH COPY renderers from one table, so the
// text and binary halves cannot answer the same column differently — the
// sibling-paths rule (.ralph/PROMPT.md hard-won rule #2) that the 53rd-56th
// slices kept finding defects with.
type bpcharWidthCase struct {
	name string
	typ  catalog.Type
	in   string
	want string
}

// bpcharWidthCases were measured against PG 18.3: `COPY zz TO STDOUT` piped
// through `cat -A`, and `COPY zz TO STDOUT (FORMAT binary)` through `xxd`, on a
// `char(10)`/`char(3)` table. bpcharsend IS textsend, so the two formats carry
// the SAME bytes — which is exactly why one table drives both.
var bpcharWidthCases = []bpcharWidthCase{
	{"char(10) short pads to 10", catalog.Type{Name: "char", Args: []int64{10}}, "ab", "ab        "},
	{"char(10) empty pads to 10", catalog.Type{Name: "char", Args: []int64{10}}, "", "          "},
	{"char(10) exact unchanged", catalog.Type{Name: "char", Args: []int64{10}}, "abcdefghij", "abcdefghij"},
	{"char(3) short pads to 3", catalog.Type{Name: "char", Args: []int64{3}}, "x", "x  "},
	{"bpchar(4) pads", catalog.Type{Name: "bpchar", Args: []int64{4}}, "hi", "hi  "},
	{"multibyte pads by rune count", catalog.Type{Name: "char", Args: []int64{5}}, "あい", "あい   "},
	{"bare char (OID 18) does not pad", catalog.Type{Name: "char"}, "x", "x"},
	{"varchar(10) does not pad", catalog.Type{Name: "varchar", Args: []int64{10}}, "ab", "ab"},
	{"text does not pad", catalog.Type{Name: "text"}, "ab", "ab"},
}

// TestCopyTextBpcharCarriesDeclaredWidth pins the TEXT/CSV half. Upstream's
// CopyOneRowTo calls the column's output function, and bpcharout is a bare
// TextDatumGetCString that trims NOTHING (postgres/src/backend/utils/adt/
// varchar.c) — so PG writes all N characters where goopg, which stores the
// value trimmed, used to write only the significant ones.
func TestCopyTextBpcharCarriesDeclaredWidth(t *testing.T) {
	for _, tc := range bpcharWidthCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := datumToCopyText(tc.typ, NewStringDatum(tc.in), "ISO", "MDY", "", "hex", nil, false)
			if err != nil {
				t.Fatalf("datumToCopyText: %v", err)
			}
			if got != tc.want {
				t.Errorf("datumToCopyText(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
		})
	}
}

// TestCopyBinaryBpcharCarriesDeclaredWidth pins the BINARY half. The field is
// length-prefixed, so the padding is observable as the field LENGTH too: a
// `char(10)` field is 10 bytes on the wire, and a reader that trusts the length
// (every real PG client does) sees a value of the wrong width otherwise.
func TestCopyBinaryBpcharCarriesDeclaredWidth(t *testing.T) {
	for _, tc := range bpcharWidthCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := datumToCopyBinary(tc.typ, NewStringDatum(tc.in))
			if err != nil {
				t.Fatalf("datumToCopyBinary: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("datumToCopyBinary(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
			if len(got) != len(tc.want) {
				t.Errorf("binary field length %d, want %d", len(got), len(tc.want))
			}
		})
	}
}

// TestCopyBinaryBpcharRoundTripsToTrimmedStorage pins the DECODE half, which
// deliberately has no bpchar arm of its own. Upstream's bpchar_recv runs
// bpchar_input, which pads to the typmod; goopg's storage convention is the
// opposite (trimmed, so compareDatum's padding-insensitive bpchar equality and
// the compact heap image hold), and coerceTextLikeDatum is the single place
// that convention is applied — including its 22001, whose wording is
// bpchar_input's own. Adding a padding arm to copyBinaryToDatum would put a
// padded value into a column an INSERT stores trimmed, i.e. make the same
// column two different widths depending on how it was loaded.
func TestCopyBinaryBpcharRoundTripsToTrimmedStorage(t *testing.T) {
	typ := catalog.Type{Name: "char", Args: []int64{10}}
	// A field exactly as real PG writes it.
	wire, err := datumToCopyBinary(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("datumToCopyBinary: %v", err)
	}
	if len(wire) != 10 {
		t.Fatalf("wire field is %d bytes, want 10", len(wire))
	}
	back, err := copyBinaryToDatum(typ, wire)
	if err != nil {
		t.Fatalf("copyBinaryToDatum: %v", err)
	}
	stored, err := coerceTextLikeDatum(typ, back)
	if err != nil {
		t.Fatalf("coerceTextLikeDatum: %v", err)
	}
	if stored != "ab" {
		t.Errorf("stored %q, want %q (goopg stores bpchar trimmed)", stored, "ab")
	}
	// And the over-length rule bpchar_input applies at input still fires on a
	// foreign stream whose extra characters are NOT spaces.
	if _, err := coerceTextLikeDatum(typ, NewStringDatum("abcdefghijk")); err == nil {
		t.Error("coerceTextLikeDatum accepted an 11-character char(10) value; want 22001")
	}
}

// TestValidateTypedLenMeasuresCharactersNotBytes pins pg_input_error_info's
// copy of the same declared-length rule. The 57th slice converted
// coerceTextLikeDatum from bytes to characters and left this sibling behind, so
// the two answered the same question differently — exactly the split .ralph/
// PROMPT.md hard-won rule #2 exists for.
//
// Measured on PG 18.3: `pg_input_error_info('あいうえお','varchar(5)')` and
// `pg_input_error_info('あいうえ','char(5)')` are both all-NULL (accepted),
// while `pg_input_error_info('abcdef','varchar(5)')` reports 22001.
// M0119-0006 (58th slice).
func TestValidateTypedLenMeasuresCharactersNotBytes(t *testing.T) {
	// The multibyte rows are the ones a byte count rejected: 5 runes of kana is
	// 15 bytes, 4 is 12. "bpchar"/"text" carry no explicit width so there is
	// nothing to check, and char(2) strips trailing blanks before checking.
	accept := []struct{ v, typ string }{
		{"あいうえお", "varchar(5)"},
		{"あいうえ", "char(5)"},
		{"あいうえお", "character(5)"},
		{"abcde", "character varying(5)"},
		{"abc", "bpchar"},
		{"abcdef", "text"},
		{"ab   ", "char(2)"},
	}
	for _, tc := range accept {
		if msg, code := validateTypedLen(tc.v, tc.typ, nil, 0); msg != "" || code != "" {
			t.Errorf("validateTypedLen(%q, %q) = (%q, %q), want it accepted", tc.v, tc.typ, msg, code)
		}
	}

	reject := []struct{ v, typ, want string }{
		{"abcdef", "varchar(5)", "character varying(5)"},
		{"あいうえおか", "varchar(5)", "character varying(5)"},
		{"あいうえおか", "char(5)", "character(5)"},
		{"abcdef", "bpchar(5)", "character(5)"},
	}
	for _, tc := range reject {
		msg, code := validateTypedLen(tc.v, tc.typ, nil, 0)
		if code != "22001" {
			t.Errorf("validateTypedLen(%q, %q) code = %q, want 22001", tc.v, tc.typ, code)
			continue
		}
		if !strings.Contains(msg, tc.want) {
			t.Errorf("validateTypedLen(%q, %q) message %q, want it to name %q", tc.v, tc.typ, msg, tc.want)
		}
	}
}

// TestValidateTypedLenResolvesTypeText pins the 59th-slice fix: the type text
// is RESOLVED (schema qualification, whitespace, domains) rather than
// prefix-matched. Before the fix, every row below silently validated NOTHING
// and reported the input as valid, where PG raises 22001.
func TestValidateTypedLenResolvesTypeText(t *testing.T) {
	cat := catalog.NewInMemory()
	// A domain over varchar(3): its Base already carries Name+Args. Registered
	// under dbOid 0 so the dbOid-0 LookupDomain below finds it (domains are
	// keyed by (dbOid, name)).
	if _, err := cat.RegisterDomain("nickname", catalog.Type{Name: "varchar", Args: []int64{3}}, false, 0); err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	// Schema-qualified / whitespace-padded / domain spellings must all resolve
	// to varchar(3) (or char(2)) and reject an over-long value.
	reject := []struct{ v, typ, want string }{
		{"abcd", "pg_catalog.varchar(3)", "character varying(3)"},
		{"abcd", "public.varchar(3)", "character varying(3)"},
		{"abcd", "varchar (3)", "character varying(3)"},
		{"abcd", "character varying(3)", "character varying(3)"},
		{"abcd", "nickname", "character varying(3)"}, // domain over varchar(3)
		{"abc", "pg_catalog.char(2)", "character(2)"},
	}
	for _, tc := range reject {
		msg, code := validateTypedLen(tc.v, tc.typ, cat, 0)
		if code != "22001" {
			t.Errorf("validateTypedLen(%q, %q) code = %q, want 22001", tc.v, tc.typ, code)
			continue
		}
		if !strings.Contains(msg, tc.want) {
			t.Errorf("validateTypedLen(%q, %q) message %q, want it to name %q", tc.v, tc.typ, msg, tc.want)
		}
	}

	// The same spellings accept an in-range value, and a schema-qualified or
	// whitespace-padded bare type still resolves (no length to check).
	accept := []struct{ v, typ string }{
		{"abc", "pg_catalog.varchar(3)"},
		{"abc", "varchar (3)"},
		{"abc", "nickname"}, // domain over varchar(3)
		{"abcdef", "pg_catalog.text"},
	}
	for _, tc := range accept {
		if msg, code := validateTypedLen(tc.v, tc.typ, cat, 0); msg != "" || code != "" {
			t.Errorf("validateTypedLen(%q, %q) = (%q, %q), want it accepted", tc.v, tc.typ, msg, code)
		}
	}
}

// TestCoerceTextLikeDatumUnboundedBpchar pins the difference between the two
// typmod-less spellings, which goopg used to collapse into one.
//
// Upstream, the implicit length of 1 belongs to the GRAMMAR, not the type:
// `char`/`character` reduce to bpchar with typmod 1 (gram.y CHARACTER
// opt_charset), while the internal name `bpchar` spelled directly carries
// typmod -1, and bpchar_input's `atttypmod < VARHDRSZ` arm then sets maxlen to
// the value's own length — no truncation, no 22001, and no trailing-blank
// strip. Measured on PG 18.3 with `CREATE TABLE t(a bpchar, b char, c character,
// d char(6))`: atttypmod is -1/5/5/10, `a` accepts 'abc', and `a` holding
// 'ab  ' is octet_length 4 where `d` holding the same is 6.
//
// goopg applied the length-1 default to all three, so `INSERT INTO t(c bpchar)
// VALUES ('abc')` raised a spurious 22001.
func TestCoerceTextLikeDatumUnboundedBpchar(t *testing.T) {
	// An unbounded bpchar accepts any length and keeps the value VERBATIM —
	// including trailing blanks, which have no declared width to be re-padded
	// from at the render boundaries (catalog.PadBpchar returns a no-Args value
	// unchanged), so trimming here would destroy them rather than defer them.
	unbounded := catalog.Type{Name: "bpchar"}
	for _, in := range []string{"abc", "", "x", "ab  ", "あいうえお", strings.Repeat("z", 300)} {
		got, err := coerceTextLikeDatum(unbounded, NewStringDatum(in))
		if err != nil {
			t.Errorf("coerceTextLikeDatum(bpchar, %q): %v; want it accepted (typmod -1 is unlimited)", in, err)
			continue
		}
		if got != in {
			t.Errorf("coerceTextLikeDatum(bpchar, %q) = %q, want it verbatim", in, got)
		}
		if padded := catalog.PadBpchar(unbounded, got); padded != in {
			t.Errorf("PadBpchar(bpchar, %q) = %q, want it verbatim (no width to pad to)", got, padded)
		}
	}

	// The grammar spellings really are character(1) and must keep erroring —
	// the default they rest on is what the bpchar gate above carves out of.
	for _, name := range []string{"char", "character"} {
		typ := catalog.Type{Name: name}
		if _, err := coerceTextLikeDatum(typ, NewStringDatum("abc")); err == nil {
			t.Errorf("coerceTextLikeDatum(%s, \"abc\") accepted; want 22001 (bare %s is character(1))", name, name)
		}
		got, err := coerceTextLikeDatum(typ, NewStringDatum("x"))
		if err != nil || got != "x" {
			t.Errorf("coerceTextLikeDatum(%s, \"x\") = %q, %v; want \"x\", nil", name, got, err)
		}
	}

	// An explicit width still bounds and still trims, whichever name spells it:
	// the trimmed storage convention is unchanged for width-carrying columns,
	// which is what compareDatum's padding-insensitive bpchar equality rests on.
	for _, name := range []string{"bpchar", "char", "character"} {
		typ := catalog.Type{Name: name, Args: []int64{3}}
		if _, err := coerceTextLikeDatum(typ, NewStringDatum("abcd")); err == nil {
			t.Errorf("coerceTextLikeDatum(%s(3), \"abcd\") accepted; want 22001", name)
		}
		got, err := coerceTextLikeDatum(typ, NewStringDatum("ab "))
		if err != nil {
			t.Errorf("coerceTextLikeDatum(%s(3), \"ab \"): %v", name, err)
			continue
		}
		if got != "ab" {
			t.Errorf("coerceTextLikeDatum(%s(3), \"ab \") = %q, want %q (stored trimmed)", name, got, "ab")
		}
		if padded := catalog.PadBpchar(typ, got); padded != "ab " {
			t.Errorf("PadBpchar(%s(3), %q) = %q, want %q", name, got, padded, "ab ")
		}
	}
}

// TestCoerceTextLikeDatumMeasuresCharactersNotBytes pins the unit the declared
// length is counted in. Upstream varchar_input and bpchar_input both measure
// with pg_mbstrlen_with_len before converting maxlen to a byte length
// (postgres/src/backend/utils/adt/varchar.c). Measured on PG 18.3:
// `octet_length('あいうえお'::varchar(5))` = 15 (accepted) and
// `octet_length('あい'::char(5))` = 9 with `length()` = 2. Counting BYTES here
// rejected both with a spurious 22001 — and disagreed with the rune-counting
// truncation the explicit-cast path in expr.go already performed and with the
// rune-counting pad catalog.PadBpchar applies on the way out.
func TestCoerceTextLikeDatumMeasuresCharactersNotBytes(t *testing.T) {
	accept := []struct {
		typ catalog.Type
		in  string
	}{
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "あいうえお"}, // 5 runes, 15 bytes
		{catalog.Type{Name: "char", Args: []int64{5}}, "あい"},      // 2 runes, 6 bytes
		{catalog.Type{Name: "char", Args: []int64{2}}, "あい"},      // exact fit
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "abcde"},
	}
	for _, tc := range accept {
		got, err := coerceTextLikeDatum(tc.typ, NewStringDatum(tc.in))
		if err != nil {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q [%d runes, %d bytes]): %v",
				tc.typ.Name, tc.typ.Args[0], tc.in, utf8.RuneCountInString(tc.in), len(tc.in), err)
			continue
		}
		if got != tc.in {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) = %q, want it unchanged",
				tc.typ.Name, tc.typ.Args[0], tc.in, got)
		}
	}

	reject := []struct {
		typ  catalog.Type
		in   string
		want string
	}{
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "あいうえおか", "character varying(5)"},
		{catalog.Type{Name: "char", Args: []int64{2}}, "あいう", "character(2)"},
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "abcdef", "character varying(5)"},
	}
	for _, tc := range reject {
		_, err := coerceTextLikeDatum(tc.typ, NewStringDatum(tc.in))
		if err == nil {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) accepted; want 22001",
				tc.typ.Name, tc.typ.Args[0], tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) error %q, want it to name %q",
				tc.typ.Name, tc.typ.Args[0], tc.in, err.Error(), tc.want)
		}
	}
}

// TestOctetBitLengthRespectBpcharDeclaredWidth pins the 65th-slice fix: PG's
// octet_length(bpchar) is bpcharoctetlen, which returns the blank-PADDED datum
// size (postgres/src/backend/utils/adt/varchar.c), while bit_length(bpchar)
// resolves through the implicit bpchar→text cast and therefore sees the TRIMMED
// value × 8 (system_functions.sql: bit_length(text) = octet_length($1)*8).
// Measured on PG 18.3: octet_length('ab'::char(10)) = 10 but
// bit_length('ab'::char(10)) = 16. goopg stores bpchar trimmed (M0103-0007),
// so octet_length pads up to the declared typmod and bit_length reads the
// trimmed byte length directly. bit_length raised "function bit_length does not
// exist" for EVERY argument before this slice.
func TestOctetBitLengthRespectBpcharDeclaredWidth(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want int64
	}{
		// text / bytea baselines (unchanged by the fix).
		{`select octet_length('abc')`, 3},
		{`select bit_length('abc')`, 24},
		{`select bit_length('\x01020304'::bytea)`, 32}, // 4 bytes × 8
		{`select octet_length('\xaabb'::bytea)`, 2},
		// bpchar is blank-padded in the datum for octet_length…
		{`select octet_length('ab'::char(10))`, 10}, // pre-fix: 2 (trimmed)
		{`select octet_length(''::char(10))`, 10},
		{`select octet_length(''::char)`, 1},        // bare char is char(1)
		{`select octet_length('ab'::char(1))`, 1},   // truncates then pads
		{`select octet_length('あ'::char(5))`, 7},   // 3-byte rune + 4 pad spaces
		// …but bit_length sees the trimmed value (the implicit bpchar→text cast
		// that resolves bit_length(bpchar) trims trailing spaces).
		{`select bit_length('ab'::char(10))`, 16}, // 2 bytes × 8, not 80
		{`select bit_length('ab'::char(1))`, 8},
		{`select bit_length(''::char)`, 0},
		{`select bit_length('あ'::char(5))`, 24},
		// An explicit bpchar→text cast trims for both.
		{`select octet_length('ab'::char(10)::text)`, 2},
		{`select bit_length('ab'::char(10)::text)`, 16},
		// A bpchar-typed column/subquery column (ColumnRef.Type carries the
		// declared width and typmod).
		{`select octet_length(c) from (values ('ab'::char(10))) v(c)`, 10},
		{`select bit_length(c) from (values ('ab'::char(10))) v(c)`, 16},
		// coalesce keeps the bpchar type, so the width still applies.
		{`select octet_length(coalesce(c, '')) from (values ('ab'::char(10))) v(c)`, 10},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindInt || d.Int != tc.want {
				t.Errorf("= %v (kind %d), want %d (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}

	// PG defines no octet_length/bit_length for non-string types: 42883. The
	// pre-fix builtins silently answered 0 for these.
	for _, sql := range []string{
		`select octet_length(5)`,
		`select bit_length(5)`,
	} {
		t.Run(sql, func(t *testing.T) {
			if ee := byteaExprErr(t, ctx, sql); ee.Code != "42883" {
				t.Errorf("%q: code = %q, want 42883", sql, ee.Code)
			}
		})
	}
}

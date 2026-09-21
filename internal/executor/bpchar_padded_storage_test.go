package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBpcharStoredPaddedAndRenderBoundariesStayInert is M0143-0007b slice 1's
// own contract plus its sibling audit, in one place because the two only mean
// something together.
//
// The contract: a width-carrying bpchar is STORED blank-padded to its declared
// width, as upstream's bpchar_input does
// (postgres/src/backend/utils/adt/varchar.c). goopg stored it trimmed until
// 2026-09-21, and that was the whole of the K41 `relpages` gap M0143-0007
// measured on `customer`/`item`.
//
// The sibling audit: the render boundaries that used to PUT the padding back
// must now be inert, and must still be there. `catalog.PadBpchar` pads only a
// short value, so every caller becomes a no-op on a padded datum — which is
// exactly what lets pre-existing TRIMMED heap images (written before the flip)
// keep rendering correctly alongside newly written padded ones. Deleting those
// calls as "now redundant" would break every old row, so this test pins them
// as inert rather than absent.
func TestBpcharStoredPaddedAndRenderBoundariesStayInert(t *testing.T) {
	typ := catalog.Type{Name: "char", Args: []int64{10}}

	stored, err := coerceTextLikeDatum(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum: %v", err)
	}
	if stored != "ab        " || len(stored) != 10 {
		t.Fatalf("stored %q (%d bytes), want a 10-byte blank-padded image", stored, len(stored))
	}

	// Render boundary 1: PadBpchar itself, the helper all four callers share.
	if got := catalog.PadBpchar(typ, stored); got != stored {
		t.Errorf("PadBpchar on a padded datum = %q, want it unchanged — the render "+
			"callers must be no-ops now, not double-padders", got)
	}

	// Render boundary 2: COPY … TO (FORMAT binary). PG writes a full-width
	// field for a char(10); so must goopg, from either image.
	wire, err := datumToCopyBinary(typ, NewStringDatum(stored))
	if err != nil {
		t.Fatalf("datumToCopyBinary: %v", err)
	}
	if len(wire) != 10 {
		t.Errorf("COPY binary field is %d bytes, want 10", len(wire))
	}

	// The pre-flip image must still render at full width. This is the case
	// that makes removing the PadBpchar calls a data-losing change rather than
	// a cleanup: rows written before 2026-09-21 are on disk trimmed.
	legacy, err := datumToCopyBinary(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("datumToCopyBinary(legacy trimmed image): %v", err)
	}
	if len(legacy) != 10 {
		t.Errorf("COPY binary field for a pre-flip TRIMMED image is %d bytes, want 10 — "+
			"old rows must keep rendering at the declared width", len(legacy))
	}

	// An UNBOUNDED bpchar has no width to pad to and is stored verbatim:
	// trailing blanks in it are data. Measured on PG 18.3, `bpchar` holding
	// 'ab  ' is octet_length 4 where char(6) holding the same is 6.
	unbounded := catalog.Type{Name: "bpchar"}
	got, err := coerceTextLikeDatum(unbounded, NewStringDatum("ab  "))
	if err != nil {
		t.Fatalf("coerceTextLikeDatum(bpchar, \"ab  \"): %v", err)
	}
	if got != "ab  " {
		t.Errorf("unbounded bpchar stored %q, want %q verbatim", got, "ab  ")
	}
}

// TestBpcharStoredColumnLengthFamilyMatchesPG pins the four length-family
// answers for a bpchar read back from a STORED column, which is where slices 1
// and 2 changed the datum under every one of them.
//
// The literal-expression forms are already covered by
// TestOctetBitLengthRespectBpcharDeclaredWidth, and they kept passing
// throughout — a literal never reaches `coerceTextLikeDatum`, so its datum
// stayed trimmed and the old accidental agreement held. Only the stored path
// diverged, and only a stored-column witness could see it.
//
// Measured against PostgreSQL 18.3 on `CREATE TABLE b(c char(10));
// INSERT INTO b VALUES('ab')`:
//
//	length(c)         2    bpcharlen strips trailing blanks (bcTruelen)
//	octet_length(c)  10    bpcharoctetlen reports the padded datum size
//	bit_length(c)    16    resolves via the implicit bpchar->text cast, which
//	                       is rtrim1 (pg_proc.dat oid 401) — so 2 bytes x 8
//	length(c::text)   2    the same rtrim1 cast, written explicitly
//
// Three of those four are trimmed answers on a padded datum, which is why the
// padding flip needed a fix at each of `length`, `bit_length` and the cast
// rather than one central place: upstream genuinely treats them differently.
func TestBpcharStoredColumnLengthFamilyMatchesPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		`create table bpad(c char(10))`,
		`insert into bpad values ('ab')`,
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	cases := []struct {
		sql  string
		want int64
	}{
		{`select length(c) from bpad`, 2},
		{`select octet_length(c) from bpad`, 10},
		{`select bit_length(c) from bpad`, 16},
		{`select length(c::text) from bpad`, 2},
		{`select octet_length(c::text) from bpad`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindInt || d.Int != tc.want {
				t.Errorf("= %v (kind %d), want %d (PG 18.3)", d.Format(), d.Kind, tc.want)
			}
		})
	}
}

// TestBpcharCastAndTextFunctionsMatchPG pins the bpchar PRODUCER/CONSUMER
// symmetry that M0143-0007b's storage flip broke, measured against PG 18.3.
//
// Slice 1 made stored bpchar blank-padded, which left two paths disagreeing
// with storage and therefore with each other:
//
//   - the CAST *to* `char(n)` still produced a TRIMMED image, so a cast value
//     and a stored value of the same logical value were different strings.
//     Upstream pads there (`bpchar()`, varchar.c) — `varchar->bpchar` is
//     `castfunc => '0'` in pg_cast.dat, so the padding comes from the typmod
//     coercion, not a cast function.
//   - `lower`/`upper` consumed the padded image. PG has no `lower(bpchar)`;
//     the call resolves through the implicit bpchar->text cast, which is
//     `rtrim1` (pg_cast.dat, `text(bpchar)`), so the padding is stripped
//     before the function runs.
//
// and one path that was excluded by a WRONG assumption: `bpchar->varchar` was
// held out of the rtrim arm on the reasoning that it is "a separate pg_cast
// entry". It is a separate entry naming the SAME function (`text(bpchar)`),
// so it strips too.
//
// These were caught by the upstream regress cases `select_having`,
// `select_implicit` and `union` (AI-20260922-004850-016), not by any value
// gate — in two of the three the VALUES were already correct and only the
// rendered column WIDTH was wrong. The `union` one was the dangerous shape:
// two representations of one value made `UNION` stop de-duplicating, so rows
// were silently doubled.
func TestBpcharCastAndTextFunctionsMatchPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		`create table bcast(c char(4), v varchar(10))`,
		`insert into bcast values ('a', 'a')`,
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	cases := []struct {
		name string
		sql  string
		want int64
	}{
		// A cast TO char(4) pads, so it is octet_length 4 — the same image the
		// stored column holds. Before the fix this was 1, which is what made
		// the cast value and the stored value compare unequal.
		// NOTE: octet_length is NOT a valid witness for the cast padding --
		// it is one of the PadBpchar RE-PADDING render callers, so it reports
		// 4 whether or not the cast padded, and a test written on it passes
		// with the fix disabled. The witness has to observe the raw image.
		// UNION de-duplication does: it compares the cast value against the
		// STORED value, so it yields one row only if the two images are
		// byte-identical. This is the shape the upstream `union` regress case
		// actually failed on.
		{"cast image equals stored image",
			`select count(*) from (select v::char(4) as x from bcast union select c as x from bcast) t`, 1},
		{"stored char(n) is padded", `select octet_length(c) from bcast`, 4},
		// varchar does NOT pad — the same typmod arm must not over-apply.
		{"cast to varchar(n) does not pad", `select octet_length(v::varchar(4)) from bcast`, 1},
		// bpchar -> varchar strips, exactly as bpchar -> text does.
		{"char(n) to varchar strips", `select octet_length(c::varchar) from bcast`, 1},
		{"char(n) to text strips", `select octet_length(c::text) from bcast`, 1},
		// lower/upper resolve through the bpchar->text cast, so they strip.
		{"lower(char(n)) strips", `select octet_length(lower(c)) from bcast`, 1},
		{"upper(char(n)) strips", `select octet_length(upper(c)) from bcast`, 1},
		// initcap is a GUARD, not a witness: verified by disabling
		// bpcharArgAsText, this case passes either way because initCap's own
		// word-splitting already drops the trailing blanks. The explicit call
		// is kept so the three-function family states the rule uniformly
		// rather than relying on one member's internals.
		{"initcap(char(n)) strips", `select octet_length(initcap(c)) from bcast`, 1},
		// A genuine varchar argument is untouched by the bpchar rule.
		{"lower(varchar) untouched", `select octet_length(lower(v)) from bcast`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindInt || d.Int != tc.want {
				t.Errorf("%s = %v (kind %d), want %d (PG 18.3)",
					tc.sql, d.Format(), d.Kind, tc.want)
			}
		})
	}
}

package initdb

import "testing"

// TestCleanEncodingName pins clean_encoding_name's lowercase + alnum-only
// normalization (encnames.c). This is what lets "UTF-8", "utf_8", and "UTF8"
// all collapse to the same alias-table key.
func TestCleanEncodingName(t *testing.T) {
	cases := map[string]string{
		"UTF-8":      "utf8",
		"utf8":       "utf8",
		"UTF_8":      "utf8",
		"ISO 8859-1": "iso88591",
		"  Latin1  ": "latin1",
		"SQL_ASCII":  "sqlascii",
		"":           "",
	}
	for in, want := range cases {
		if got := cleanEncodingName(in); got != want {
			t.Errorf("cleanEncodingName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPgCharToEncoding checks alias resolution against pg_char_to_encoding,
// including the NAMEDATALEN length guard and unknown-name rejection.
func TestPgCharToEncoding(t *testing.T) {
	cases := []struct {
		name string
		want int32
	}{
		{"UTF8", pgEncUTF8},
		{"utf-8", pgEncUTF8},
		{"unicode", pgEncUTF8},
		{"SQL_ASCII", pgEncSQLASCII},
		{"latin1", pgEncLATIN1},
		{"ISO8859-1", pgEncLATIN1},
		{"sjis", pgEncSJIS},
		{"WIN950", pgEncBIG5},
		{"euc_jp", pgEncEUCJP},
		{"koi8", pgEncKOI8R},
		{"bogus-encoding", -1},
		{"", -1},
	}
	for _, c := range cases {
		if got := pgCharToEncoding(c.name); got != c.want {
			t.Errorf("pgCharToEncoding(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	// NAMEDATALEN guard: a 64+ byte name is rejected outright.
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	if got := pgCharToEncoding(string(long)); got != -1 {
		t.Errorf("pgCharToEncoding(64-byte name) = %d, want -1", got)
	}
}

// TestPgValidServerEncoding pins PG_VALID_BE_ENCODING: encodings 0..KOI8U(34)
// are valid server-side; the client-only encodings above that are rejected
// even though they resolve to a known ID.
func TestPgValidServerEncoding(t *testing.T) {
	valid := []string{"SQL_ASCII", "UTF8", "LATIN1", "EUC_JP", "KOI8U", "WIN1258"}
	for _, name := range valid {
		if got := pgValidServerEncoding(name); got < 0 {
			t.Errorf("pgValidServerEncoding(%q) = %d, want >= 0", name, got)
		}
	}
	// Client-only encodings (ID > PG_KOI8U): recognized but not server-valid.
	clientOnly := []string{"SJIS", "BIG5", "GBK", "UHC", "GB18030", "JOHAB", "SHIFT_JIS_2004"}
	for _, name := range clientOnly {
		if pgCharToEncoding(name) < 0 {
			t.Fatalf("precondition: %q should resolve to a known encoding", name)
		}
		if got := pgValidServerEncoding(name); got != -1 {
			t.Errorf("pgValidServerEncoding(%q) = %d, want -1 (client-only)", name, got)
		}
	}
}

// TestPgEncodingToChar checks the ID→canonical-name table round-trips with
// pgValidServerEncoding and rejects out-of-range IDs.
func TestPgEncodingToChar(t *testing.T) {
	if got := pgEncodingToChar(pgEncUTF8); got != "UTF8" {
		t.Errorf("pgEncodingToChar(UTF8) = %q, want %q", got, "UTF8")
	}
	if got := pgEncodingToChar(pgEncSQLASCII); got != "SQL_ASCII" {
		t.Errorf("pgEncodingToChar(SQL_ASCII) = %q, want %q", got, "SQL_ASCII")
	}
	if got := pgEncodingToChar(pgEncSHIFTJIS2004); got != "SHIFT_JIS_2004" {
		t.Errorf("pgEncodingToChar(SHIFT_JIS_2004) = %q, want %q", got, "SHIFT_JIS_2004")
	}
	if got := pgEncodingToChar(-1); got != "" {
		t.Errorf("pgEncodingToChar(-1) = %q, want empty", got)
	}
	if got := pgEncodingToChar(int32(len(pgEncNames))); got != "" {
		t.Errorf("pgEncodingToChar(out-of-range) = %q, want empty", got)
	}
}

// TestResolveEncoding covers get_encoding_id's three outcomes: empty → UTF8
// default, a valid name → its ID, an invalid/client-only name → initdb's
// "is not a valid server encoding name" error.
func TestResolveEncoding(t *testing.T) {
	if got, err := resolveEncoding(""); err != nil || got != pgEncUTF8 {
		t.Errorf("resolveEncoding(\"\") = (%d, %v), want (%d, nil)", got, err, pgEncUTF8)
	}
	if got, err := resolveEncoding("LATIN1"); err != nil || got != pgEncLATIN1 {
		t.Errorf("resolveEncoding(LATIN1) = (%d, %v), want (%d, nil)", got, err, pgEncLATIN1)
	}
	if got, err := resolveEncoding("UTF-8"); err != nil || got != pgEncUTF8 {
		t.Errorf("resolveEncoding(UTF-8) = (%d, %v), want (%d, nil)", got, err, pgEncUTF8)
	}
	for _, bad := range []string{"SJIS", "BIG5", "not-an-encoding"} {
		if _, err := resolveEncoding(bad); err == nil {
			t.Errorf("resolveEncoding(%q) = nil error, want rejection", bad)
		}
	}
}

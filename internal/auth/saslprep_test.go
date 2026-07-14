package auth

import "testing"

// TestPGSASLPrepRFC4013Examples pins pgSASLPrep to the canonical example
// table in RFC 4013 §3 ("Examples"), the same reference upstream's own
// pg_saslprep implements.
func TestPGSASLPrepRFC4013Examples(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		// "I­X" -> "IX": SOFT HYPHEN is "commonly mapped to nothing".
		{name: "soft hyphen dropped", input: "I­X", want: "IX"},
		{name: "unchanged ascii lower", input: "user", want: "user"},
		{name: "unchanged ascii upper (no case folding)", input: "USER", want: "USER"},
		// U+00AA FEMININE ORDINAL INDICATOR NFKC-decomposes to "a".
		{name: "compatibility-decomposes to a", input: "ª", want: "a"},
		// U+2168 ROMAN NUMERAL NINE NFKC-decomposes to "IX".
		{name: "compatibility-decomposes to IX", input: "Ⅸ", want: "IX"},
		// U+2028 LINE SEPARATOR is a C.2.2/C.8 prohibited-output character.
		// (A pure-ASCII control character like BEL is NOT rejected: pg_saslprep
		// short-circuits to success for any all-ASCII input before the
		// prohibited-output check ever runs -- see pgSASLPrep's isASCII branch,
		// which mirrors upstream's own "quick check" comment verbatim.)
		{name: "non-ascii prohibited output char", input: "a b", wantErr: true},
		// U+0627 ARABIC LETTER ALEF (RandALCat) followed by "1" (neither
		// RandALCat nor the string's own first/last char alignment):
		// violates the bidi "first and last char must be RandALCat" rule.
		{name: "bidi rule violation", input: "ا1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rc := pgSASLPrep(tt.input)
			if tt.wantErr {
				if rc == saslPrepSuccess {
					t.Fatalf("pgSASLPrep(%q) = %q, want an error/prohibited result", tt.input, got)
				}
				return
			}
			if rc != saslPrepSuccess {
				t.Fatalf("pgSASLPrep(%q) failed with rc=%d, want success", tt.input, rc)
			}
			if got != tt.want {
				t.Fatalf("pgSASLPrep(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestPGSASLPrepInvalidUTF8 confirms invalid UTF-8 is reported distinctly
// from a prohibited-characters result (pg_saslprep's SASLPREP_INVALID_UTF8
// vs SASLPREP_PROHIBITED), even though both fall back to the raw password
// at the saslPrepOrOriginal call sites.
func TestPGSASLPrepInvalidUTF8(t *testing.T) {
	_, rc := pgSASLPrep("abc\xff\xfedef")
	if rc != saslPrepInvalidUTF8 {
		t.Fatalf("pgSASLPrep(invalid utf8) rc = %d, want saslPrepInvalidUTF8", rc)
	}
}

// TestSASLPrepOrOriginalFallsBackOnProhibited matches
// pg_be_scram_build_secret's/scram_verify_plain_password's own fallback:
// SASLprep failure is not a hard error, it just leaves the password as-is.
func TestSASLPrepOrOriginalFallsBackOnProhibited(t *testing.T) {
	prohibited := "bell"
	if got := saslPrepOrOriginal(prohibited); got != prohibited {
		t.Fatalf("saslPrepOrOriginal(%q) = %q, want the unmodified input", prohibited, got)
	}
}

// TestSCRAMSecretNormalizesEquivalentUnicodeForms proves the SASLprep
// wiring in NewSCRAMSecretWithIterations/VerifySCRAMSecretFromPassword
// actually changes SCRAM's derived-key behavior, not just that pgSASLPrep
// exists as dead code: two different byte sequences that are SASLprep-
// equivalent (an NFKC-decomposable form vs. its canonical ASCII form) must
// authenticate identically, exactly like real PostgreSQL's
// pg_be_scram_build_secret / scram_verify_plain_password.
func TestSCRAMSecretNormalizesEquivalentUnicodeForms(t *testing.T) {
	// U+2168 ROMAN NUMERAL NINE SASLpreps to "IX".
	secret, err := NewSCRAMSecretWithIterations("Ⅸ", 4096)
	if err != nil {
		t.Fatalf("NewSCRAMSecretWithIterations: %v", err)
	}
	if !secret.VerifySCRAMSecretFromPassword("IX") {
		t.Fatal("a secret derived from U+2168 must verify against its canonical form \"IX\"")
	}
	if !secret.VerifySCRAMSecretFromPassword("Ⅸ") {
		t.Fatal("a secret derived from U+2168 must also verify against the original form (idempotent normalization)")
	}
	if secret.VerifySCRAMSecretFromPassword("ix") {
		t.Fatal("SASLprep does not case-fold; \"ix\" must NOT match a secret derived from U+2168 (-> \"IX\")")
	}

	// Round trip the other direction: derive from the canonical form,
	// verify against the decomposable one.
	secret2, err := NewSCRAMSecretWithIterations("IX", 4096)
	if err != nil {
		t.Fatalf("NewSCRAMSecretWithIterations: %v", err)
	}
	if !secret2.VerifySCRAMSecretFromPassword("Ⅸ") {
		t.Fatal("a secret derived from \"IX\" must verify against the NFKC-equivalent U+2168 form")
	}
}

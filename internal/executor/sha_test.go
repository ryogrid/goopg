package executor

// M0134-0070 — the four SHA digest builtins (pg_proc OIDs 3419-3422,
// RetType 17=bytea, arg bytea) must return a KindBytes datum on the wire as
// `\x…`. sha256/sha512 used to return hex TEXT (KindString); sha224/sha384
// fell through the evalFuncCall switch entirely ("function does not exist").
// All expected values below are PG 18.3 oracle output, 1:1 from
// postgres/src/test/regress/expected/strings.out:2334-2380 (the empty-string
// and "The quick brown fox…" inputs for all four, 28/32/48/64 bytes).

import (
	"encoding/hex"
	"testing"
)

func TestSha224(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql     string
		wantHex string
	}{
		{`select sha224('')`, "d14a028c2a3a2bc9476102bb288234c415a2b01f828ea62ac5b3e42f"},
		{`select sha224('The quick brown fox jumps over the lazy dog.')`, "619cba8e8e05826e9b8c519c0a5c68f4fb653e8a3d8aa04bb2c8cd4c"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind = %d, want KindBytes (%d)", d.Kind, KindBytes)
			}
			if got := hex.EncodeToString(d.BytesValue()); got != tc.wantHex {
				t.Errorf("digest = %s (%d bytes), want %s (28 bytes, PG 18.3)", got, len(got)/2, tc.wantHex)
			}
			if colType != "bytea" {
				t.Errorf("advertised column type = %q, want bytea — a KindBytes datum "+
					"typed as anything else reaches the wire as raw bytes", colType)
			}
		})
	}
}

func TestSha256(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql     string
		wantHex string
	}{
		{`select sha256('')`, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{`select sha256('The quick brown fox jumps over the lazy dog.')`, "ef537f25c895bfa782526529a9b63d97aa631564d5d789c2b765448c8635fb6c"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind = %d, want KindBytes (%d)", d.Kind, KindBytes)
			}
			if got := hex.EncodeToString(d.BytesValue()); got != tc.wantHex {
				t.Errorf("digest = %s (%d bytes), want %s (32 bytes, PG 18.3)", got, len(got)/2, tc.wantHex)
			}
			if colType != "bytea" {
				t.Errorf("advertised column type = %q, want bytea — a KindBytes datum "+
					"typed as anything else reaches the wire as raw bytes", colType)
			}
		})
	}
}

func TestSha384(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql     string
		wantHex string
	}{
		{`select sha384('')`, "38b060a751ac96384cd9327eb1b1e36a21fdb71114be07434c0cc7bf63f6e1da274edebfe76f65fbd51ad2f14898b95b"},
		{`select sha384('The quick brown fox jumps over the lazy dog.')`, "ed892481d8272ca6df370bf706e4d7bc1b5739fa2177aae6c50e946678718fc67a7af2819a021c2fc34e91bdb63409d7"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind = %d, want KindBytes (%d)", d.Kind, KindBytes)
			}
			if got := hex.EncodeToString(d.BytesValue()); got != tc.wantHex {
				t.Errorf("digest = %s (%d bytes), want %s (48 bytes, PG 18.3)", got, len(got)/2, tc.wantHex)
			}
			if colType != "bytea" {
				t.Errorf("advertised column type = %q, want bytea — a KindBytes datum "+
					"typed as anything else reaches the wire as raw bytes", colType)
			}
		})
	}
}

func TestSha512(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql     string
		wantHex string
	}{
		{`select sha512('')`, "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"},
		{`select sha512('The quick brown fox jumps over the lazy dog.')`, "91ea1245f20d46ae9a037a989f54f1f790f0a47607eeb8a14d12890cea77a1bbc6c7ed9cf205e67b7f2b8fd4c7dfd3a7a8617e45f3c463d481c7e586c39ac1ed"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind = %d, want KindBytes (%d)", d.Kind, KindBytes)
			}
			if got := hex.EncodeToString(d.BytesValue()); got != tc.wantHex {
				t.Errorf("digest = %s (%d bytes), want %s (64 bytes, PG 18.3)", got, len(got)/2, tc.wantHex)
			}
			if colType != "bytea" {
				t.Errorf("advertised column type = %q, want bytea — a KindBytes datum "+
					"typed as anything else reaches the wire as raw bytes", colType)
			}
		})
	}
}

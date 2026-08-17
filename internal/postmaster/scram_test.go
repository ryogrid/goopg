package postmaster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq/auth"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// scramClientFirst writes a SASLInitialResponse frame.
func scramClientFirst(t *testing.T, conn net.Conn, mech, clientFirst string) {
	t.Helper()
	body := []byte(mech)
	body = append(body, 0)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(clientFirst)))
	body = append(body, lenBuf[:]...)
	body = append(body, clientFirst...)
	hdr := make([]byte, 5)
	hdr[0] = libpq.MsgPasswordMessage
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := conn.Write(append(hdr, body...)); err != nil {
		t.Fatalf("write SASLInitialResponse: %v", err)
	}
}

// scramClientFinal writes a SASLResponse frame carrying client-final-message.
func scramClientFinal(t *testing.T, conn net.Conn, body string) {
	t.Helper()
	hdr := make([]byte, 5)
	hdr[0] = libpq.MsgPasswordMessage
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := conn.Write(append(hdr, []byte(body)...)); err != nil {
		t.Fatalf("write SASLResponse: %v", err)
	}
}

// pbkdf2 mirrors the helper used by the auth package; reproduced here so the
// test is self-contained.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	numBlocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, numBlocks*hashLen)
	for i := 1; i <= numBlocks; i++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		var ibuf [4]byte
		binary.BigEndian.PutUint32(ibuf[:], uint32(i))
		mac.Write(ibuf[:])
		ui := mac.Sum(nil)
		block := append([]byte(nil), ui...)
		for j := 2; j <= iter; j++ {
			m := hmac.New(sha256.New, password)
			m.Write(ui)
			ui = m.Sum(nil)
			for k := range block {
				block[k] ^= ui[k]
			}
		}
		out = append(out, block...)
	}
	return out[:keyLen]
}

func hmacS256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// TestSCRAMAuthSuccess drives an end-to-end SCRAM-SHA-256 handshake
// against a real listener, using a hand-rolled libpq-style client.
func TestSCRAMAuthSuccess(t *testing.T) {
	const username, password = "alice", "hunter2"
	cred, err := auth.NewSCRAMCredential(password)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewMapUserStore()
	store.Set(username, cred)

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 scram-sha-256\n", store)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": username})

	r := libpq.NewFrameReader(conn)

	// 1) Server sends AuthenticationSASL with mechanism list.
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationSASL: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload[:4]) != libpq.AuthenticationSASL {
		t.Fatalf("first frame = (%c, %v), want R/10", f.Type, f.Payload[:4])
	}
	mechs := strings.Split(strings.TrimRight(string(f.Payload[4:]), "\x00"), "\x00")
	found := false
	for _, m := range mechs {
		if m == "SCRAM-SHA-256" {
			found = true
		}
	}
	if !found {
		t.Fatalf("server did not advertise SCRAM-SHA-256, got %v", mechs)
	}

	// 2) Client sends SASLInitialResponse with client-first-message.
	clientNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirstBare := "n=" + username + ",r=" + clientNonce
	scramClientFirst(t, conn, "SCRAM-SHA-256", "n,,"+clientFirstBare)

	// 3) Server sends AuthenticationSASLContinue with server-first-message.
	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read SASLContinue: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload[:4]) != libpq.AuthenticationSASLCont {
		t.Fatalf("frame = (%c, %v), want R/11", f.Type, f.Payload[:4])
	}
	serverFirst := string(f.Payload[4:])

	// Parse server-first to extract combined nonce, salt, iterations.
	var combinedNonce, saltB64, iterStr string
	for _, attr := range strings.Split(serverFirst, ",") {
		switch {
		case strings.HasPrefix(attr, "r="):
			combinedNonce = attr[2:]
		case strings.HasPrefix(attr, "s="):
			saltB64 = attr[2:]
		case strings.HasPrefix(attr, "i="):
			iterStr = attr[2:]
		}
	}
	if !strings.HasPrefix(combinedNonce, clientNonce) {
		t.Fatalf("combined nonce did not extend client nonce: %q", combinedNonce)
	}
	salt, _ := base64.StdEncoding.DecodeString(saltB64)
	iter := 0
	for _, c := range iterStr {
		iter = iter*10 + int(c-'0')
	}

	// 4) Client computes client-final-message and sends SASLResponse.
	saltedPassword := pbkdf2SHA256([]byte(password), salt, iter, 32)
	clientKey := hmacS256(saltedPassword, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	gs2Header := base64.StdEncoding.EncodeToString([]byte("n,,"))
	clientFinalWithoutProof := "c=" + gs2Header + ",r=" + combinedNonce
	authMessage := clientFirstBare + "," + serverFirst + "," + clientFinalWithoutProof
	clientSignature := hmacS256(storedKey[:], []byte(authMessage))
	clientProof := make([]byte, len(clientKey))
	for i := range clientKey {
		clientProof[i] = clientKey[i] ^ clientSignature[i]
	}
	clientFinal := clientFinalWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof)
	scramClientFinal(t, conn, clientFinal)

	// 5) Server sends AuthenticationSASLFinal with v=ServerSignature.
	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read SASLFinal: %v", err)
	}
	if binary.BigEndian.Uint32(f.Payload[:4]) != libpq.AuthenticationSASLFinal {
		t.Fatalf("frame = R/%d, want R/12", binary.BigEndian.Uint32(f.Payload[:4]))
	}
	finalBody := string(f.Payload[4:])
	if !strings.HasPrefix(finalBody, "v=") {
		t.Fatalf("server-final missing v=: %q", finalBody)
	}
	serverKey := hmacS256(saltedPassword, []byte("Server Key"))
	expectedSig := hmacS256(serverKey, []byte(authMessage))
	gotSig, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(finalBody, "v="))
	if !hmac.Equal(gotSig, expectedSig) {
		t.Fatalf("ServerSignature mismatch:\n got  %x\n want %x", gotSig, expectedSig)
	}

	// 6) AuthenticationOk.
	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationOk: %v", err)
	}
	if binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationOK {
		t.Fatalf("post-SCRAM frame = R/%d, want R/0", binary.BigEndian.Uint32(f.Payload))
	}
}

// TestSCRAMAuthWrongPasswordRejected: client computes the proof from a
// wrong password; server returns FATAL/28000.
func TestSCRAMAuthWrongPasswordRejected(t *testing.T) {
	const username, realPassword, wrongPassword = "alice", "hunter2", "WRONG"
	cred, err := auth.NewSCRAMCredential(realPassword)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewMapUserStore()
	store.Set(username, cred)

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 scram-sha-256\n", store)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": username})

	r := libpq.NewFrameReader(conn)
	if _, err := r.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	clientNonce := "ABCDEFGHIJKLMNOPQR"
	clientFirstBare := "n=" + username + ",r=" + clientNonce
	scramClientFirst(t, conn, "SCRAM-SHA-256", "n,,"+clientFirstBare)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	serverFirst := string(f.Payload[4:])

	var combinedNonce, saltB64, iterStr string
	for _, attr := range strings.Split(serverFirst, ",") {
		switch {
		case strings.HasPrefix(attr, "r="):
			combinedNonce = attr[2:]
		case strings.HasPrefix(attr, "s="):
			saltB64 = attr[2:]
		case strings.HasPrefix(attr, "i="):
			iterStr = attr[2:]
		}
	}
	salt, _ := base64.StdEncoding.DecodeString(saltB64)
	iter := 0
	for _, c := range iterStr {
		iter = iter*10 + int(c-'0')
	}
	salted := pbkdf2SHA256([]byte(wrongPassword), salt, iter, 32)
	ck := hmacS256(salted, []byte("Client Key"))
	sk := sha256.Sum256(ck)
	gs2 := base64.StdEncoding.EncodeToString([]byte("n,,"))
	cfwp := "c=" + gs2 + ",r=" + combinedNonce
	authMsg := clientFirstBare + "," + serverFirst + "," + cfwp
	cs := hmacS256(sk[:], []byte(authMsg))
	proof := make([]byte, len(ck))
	for i := range ck {
		proof[i] = ck[i] ^ cs[i]
	}
	scramClientFinal(t, conn, cfwp+",p="+base64.StdEncoding.EncodeToString(proof))

	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read FATAL: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("frame = %c, want E", f.Type)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want 28000", got[libpq.FieldSQLState])
	}
}

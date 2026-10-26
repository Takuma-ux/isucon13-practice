package main

import (
	"bytes"
	"encoding/gob"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func testSessionSecret() []byte {
	return []byte("isucon13_session_cookiestore_defaultsecret")
}

func sessionValuesOldFormat(userID int64, username string, expires int64) map[interface{}]interface{} {
	return map[interface{}]interface{}{
		"SESSIONID": "550e8400-e29b-41d4-a716-446655440000",
		"USERID":    userID,
		"USERNAME":  username,
		"EXPIRES":   expires,
	}
}

func sessionValuesNewFormat(userID int64, expires int64) map[interface{}]interface{} {
	return map[interface{}]interface{}{
		"USERID":  userID,
		"EXPIRES": expires,
	}
}

func encodeSessionCookie(vals map[interface{}]interface{}) (string, error) {
	codecs := securecookie.CodecsFromPairs(testSessionSecret())
	configureSessionCodecs(codecs)
	return securecookie.EncodeMulti(defaultSessionIDKey, vals, codecs...)
}

func gobLen(vals map[interface{}]interface{}) int {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(vals); err != nil {
		panic(err)
	}
	return buf.Len()
}

func echoContextWithSessionCookie(cookieValue string) echo.Context {
	store := newSessionCookieStore(testSessionSecret())
	e := echo.New()
	e.Use(session.Middleware(store))

	var c echo.Context
	e.GET("/", func(ctx echo.Context) error {
		c = ctx
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: defaultSessionIDKey, Value: cookieValue})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if c == nil {
		panic("echo context not set")
	}
	return c
}

func TestSessionOldFormatStillAuthenticates(t *testing.T) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	encoded, err := encodeSessionCookie(sessionValuesOldFormat(1, "test001", expires))
	if err != nil {
		t.Fatalf("encode old session: %v", err)
	}

	c := echoContextWithSessionCookie(encoded)
	if err := verifyUserSession(c); err != nil {
		t.Fatalf("verifyUserSession old 4-key cookie: %v", err)
	}

	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		t.Fatalf("session.Get: %v", err)
	}
	uid, ok := sess.Values[defaultUserIDKey].(int64)
	if !ok || uid != 1 {
		t.Fatalf("USERID: got %v ok=%v", sess.Values[defaultUserIDKey], ok)
	}
	// Extra keys from old cookies must not break anything.
	if _, ok := sess.Values["USERNAME"]; !ok {
		t.Fatalf("expected legacy USERNAME key to decode")
	}
}

func TestSessionNewFormatAuthenticates(t *testing.T) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	encoded, err := encodeSessionCookie(sessionValuesNewFormat(42, expires))
	if err != nil {
		t.Fatalf("encode new session: %v", err)
	}

	c := echoContextWithSessionCookie(encoded)
	if err := verifyUserSession(c); err != nil {
		t.Fatalf("verifyUserSession new 2-key cookie: %v", err)
	}
}

func TestSessionPayloadSizeReduction(t *testing.T) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	oldVals := sessionValuesOldFormat(999, "bench_streamer_00042", expires)
	newVals := sessionValuesNewFormat(999, expires)

	oldGob, newGob := gobLen(oldVals), gobLen(newVals)
	oldCookie, err := encodeSessionCookie(oldVals)
	if err != nil {
		t.Fatalf("encode old: %v", err)
	}
	newCookie, err := encodeSessionCookie(newVals)
	if err != nil {
		t.Fatalf("encode new: %v", err)
	}

	t.Logf("gob_payload_bytes old=%d new=%d delta=%d", oldGob, newGob, oldGob-newGob)
	t.Logf("securecookie_cookie_bytes old=%d new=%d delta=%d", len(oldCookie), len(newCookie), len(oldCookie)-len(newCookie))

	if newGob >= oldGob {
		t.Fatalf("expected smaller gob payload: old=%d new=%d", oldGob, newGob)
	}
	if len(newCookie) >= len(oldCookie) {
		t.Fatalf("expected smaller cookie: old=%d new=%d", len(oldCookie), len(newCookie))
	}
}

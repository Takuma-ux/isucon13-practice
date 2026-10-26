package main

import (
	"testing"
	"time"

	"github.com/gorilla/securecookie"
)

func TestSessionJSONSerializerRoundTrip(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	userID := int64(4242)
	vals := map[interface{}]interface{}{
		defaultUserIDKey:         userID,
		defaultSessionExpiresKey: expires,
	}

	codecs := securecookie.CodecsFromPairs(testSessionSecret())
	configureSessionCodecs(codecs)

	encoded, err := securecookie.EncodeMulti(defaultSessionIDKey, vals, codecs...)
	if err != nil {
		t.Fatalf("EncodeMulti: %v", err)
	}

	var decoded map[interface{}]interface{}
	if err := securecookie.DecodeMulti(defaultSessionIDKey, encoded, &decoded, codecs...); err != nil {
		t.Fatalf("DecodeMulti: %v", err)
	}

	gotUID, ok := decoded[defaultUserIDKey].(int64)
	if !ok || gotUID != userID {
		t.Fatalf("USERID: got %v (%T) want %d", decoded[defaultUserIDKey], decoded[defaultUserIDKey], userID)
	}

	gotExp, ok := decoded[defaultSessionExpiresKey].(int64)
	if !ok || gotExp != expires {
		t.Fatalf("EXPIRES: got %v (%T) want %d", decoded[defaultSessionExpiresKey], decoded[defaultSessionExpiresKey], expires)
	}
}

package main

import (
	"fmt"
	"math"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
)

// sessionJSONSerializer uses securecookie.JSONEncoder for the wire format.
// Gorilla session Values are map[interface{}]interface{}, which encoding/json
// cannot marshal directly; we convert via map[string]interface{}.
type sessionJSONSerializer struct {
	enc securecookie.JSONEncoder
}

func (s sessionJSONSerializer) Serialize(src interface{}) ([]byte, error) {
	if m, ok := src.(map[interface{}]interface{}); ok {
		conv := make(map[string]interface{}, len(m))
		for k, v := range m {
			conv[fmt.Sprint(k)] = v
		}
		return s.enc.Serialize(conv)
	}
	return s.enc.Serialize(src)
}

func (s sessionJSONSerializer) Deserialize(src []byte, dst interface{}) error {
	pmap, ok := dst.(*map[interface{}]interface{})
	if !ok {
		return s.enc.Deserialize(src, dst)
	}
	var conv map[string]interface{}
	if err := s.enc.Deserialize(src, &conv); err != nil {
		return err
	}
	out := make(map[interface{}]interface{}, len(conv))
	for k, v := range conv {
		out[k] = sessionJSONToValue(v)
	}
	*pmap = out
	return nil
}

func sessionJSONToValue(v interface{}) interface{} {
	if f, ok := v.(float64); ok {
		if f >= math.MinInt64 && f <= math.MaxInt64 && f == math.Trunc(f) {
			return int64(f)
		}
	}
	return v
}

func configureSessionCodecs(codecs []securecookie.Codec) {
	sz := sessionJSONSerializer{}
	for _, codec := range codecs {
		if sc, ok := codec.(*securecookie.SecureCookie); ok {
			sc.SetSerializer(sz)
		}
	}
}

func newSessionCookieStore(keyPairs ...[]byte) *sessions.CookieStore {
	store := sessions.NewCookieStore(keyPairs...)
	configureSessionCodecs(store.Codecs)
	return store
}

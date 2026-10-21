package main

import (
	"bytes"
	"encoding/json"
	"encoding/base64"
	"reflect"
	"testing"

	gojson "github.com/goccy/go-json"
)

func decodeStdlib[T any](data []byte) (T, error) {
	var v T
	err := json.NewDecoder(bytes.NewReader(data)).Decode(&v)
	return v, err
}

func decodeGoccy[T any](data []byte) (T, error) {
	var v T
	err := gojson.NewDecoder(bytes.NewReader(data)).Decode(&v)
	return v, err
}

func compareDecode[T any](t *testing.T, label string, data []byte) {
	std, errStd := decodeStdlib[T](data)
	goc, errGoc := decodeGoccy[T](data)

	if (errStd == nil) != (errGoc == nil) {
		t.Fatalf("%s: error mismatch std=%v goccy=%v", label, errStd, errGoc)
	}
	if errStd != nil {
		return
	}
	if !reflect.DeepEqual(std, goc) {
		t.Fatalf("%s: value mismatch\nstdlib: %#v\ngoccy:  %#v", label, std, goc)
	}
}

func TestJSONDecodeEquivalenceRequestBodies(t *testing.T) {
	tinyPNG := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	b64Image := base64.StdEncoding.EncodeToString(tinyPNG)

	cases := []struct {
		label string
		json  string
	}{
		// LoginRequest
		{"login_minimal", `{"username":"pipe","password":"secret"}`},
		{"login_unicode", `{"username":"user_椅子","password":"パス🎉"}`},
		{"login_extra_field", `{"username":"a","password":"b","unknown":1}`},

		// PostUserRequest
		{"register_full", `{"name":"newuser","display_name":"New","description":"desc","password":"pw","theme":{"dark_mode":true}}`},
		{"register_theme_false", `{"name":"u","display_name":"U","description":"","password":"p","theme":{"dark_mode":false}}`},
		{"register_missing_theme", `{"name":"u","display_name":"U","description":"","password":"p"}`},

		// PostIconRequest
		{"icon_base64", `{"image":"` + b64Image + `"}`},
		{"icon_empty_string", `{"image":""}`},

		// PostLivecommentRequest
		{"comment_full", `{"comment":"hello 世界","tip":500}`},
		{"comment_missing_tip", `{"comment":"only text"}`},
		{"comment_zero_tip", `{"comment":"x","tip":0}`},
		{"comment_unicode_emoji", `{"comment":"🎉あ","tip":1}`},

		// PostReactionRequest
		{"reaction", `{"emoji_name":"chair"}`},
		{"reaction_unicode", `{"emoji_name":"tada"}`},

		// ModerateRequest
		{"moderate", `{"ng_word":"spamword"}`},
		{"moderate_unicode", `{"ng_word":"禁止ワード"}`},

		// ReserveLivestreamRequest
		{"reserve_full", `{"tags":[1,103],"title":"t","description":"d","playlist_url":"https://example.com/p.m3u8","thumbnail_url":"https://example.com/t.webp","start_at":1700874000,"end_at":1700877600}`},
		{"reserve_empty_tags", `{"tags":[],"title":"t","description":"d","playlist_url":"https://example.com/p.m3u8","thumbnail_url":"https://example.com/t.webp","start_at":1700874000,"end_at":1700877600}`},
		{"reserve_large_int64", `{"tags":[1],"title":"t","description":"d","playlist_url":"https://example.com/p.m3u8","thumbnail_url":"https://example.com/t.webp","start_at":9007199254740991,"end_at":9007199254740992}`},
	}

	for _, c := range cases {
		data := []byte(c.json)
		switch {
		case c.label == "login_minimal" || c.label == "login_unicode" || c.label == "login_extra_field":
			compareDecode[LoginRequest](t, c.label, data)
		case c.label == "register_full" || c.label == "register_theme_false" || c.label == "register_missing_theme":
			compareDecode[PostUserRequest](t, c.label, data)
		case c.label == "icon_base64" || c.label == "icon_empty_string":
			compareDecode[PostIconRequest](t, c.label, data)
		case c.label == "comment_full" || c.label == "comment_missing_tip" || c.label == "comment_zero_tip" || c.label == "comment_unicode_emoji":
			compareDecode[PostLivecommentRequest](t, c.label, data)
		case c.label == "reaction" || c.label == "reaction_unicode":
			compareDecode[PostReactionRequest](t, c.label, data)
		case c.label == "moderate" || c.label == "moderate_unicode":
			compareDecode[ModerateRequest](t, c.label, data)
		default:
			compareDecode[ReserveLivestreamRequest](t, c.label, data)
		}
	}
}

func TestJSONDecodeEquivalenceErrorCases(t *testing.T) {
	badJSON := []byte(`{invalid`)
	_, errStd := decodeStdlib[LoginRequest](badJSON)
	_, errGoc := decodeGoccy[LoginRequest](badJSON)
	if errStd == nil || errGoc == nil {
		t.Fatal("expected syntax error from both decoders")
	}

	typeMismatch := []byte(`{"username":"u","password":1}`)
	_, errStd = decodeStdlib[LoginRequest](typeMismatch)
	_, errGoc = decodeGoccy[LoginRequest](typeMismatch)
	if errStd == nil || errGoc == nil {
		t.Fatal("expected type error from both decoders")
	}

	wrongTip := []byte(`{"comment":"x","tip":"not-a-number"}`)
	_, errStd = decodeStdlib[PostLivecommentRequest](wrongTip)
	_, errGoc = decodeGoccy[PostLivecommentRequest](wrongTip)
	if errStd == nil || errGoc == nil {
		t.Fatal("expected type error for tip field from both")
	}
}

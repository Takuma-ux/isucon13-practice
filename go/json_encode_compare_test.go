package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	gojson "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
)

// echoEncode mimics echo.DefaultJSONSerializer.Serialize body (stdlib).
func echoEncodeStdlib(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// echoEncodeGoJSON mimics GoJSONSerializer.Serialize body (goccy).
func echoEncodeGoJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gojson.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compareJSONEncodings(t *testing.T, label string, v interface{}) {
	std, err := echoEncodeStdlib(v)
	if err != nil {
		t.Fatalf("%s: stdlib encode: %v", label, err)
	}
	goc, err := echoEncodeGoJSON(v)
	if err != nil {
		t.Fatalf("%s: go-json encode: %v", label, err)
	}

	if bytes.Equal(std, goc) {
		t.Logf("%s: byte-identical (%d bytes)", label, len(std))
		return
	}

	var stdVal, gocVal interface{}
	if err := json.Unmarshal(std, &stdVal); err != nil {
		t.Fatalf("%s: unmarshal stdlib output: %v", label, err)
	}
	if err := json.Unmarshal(goc, &gocVal); err != nil {
		t.Fatalf("%s: unmarshal go-json output: %v", label, err)
	}
	if reflect.DeepEqual(stdVal, gocVal) {
		t.Logf("%s: semantic match, bytes differ (stdlib %d, go-json %d)", label, len(std), len(goc))
		t.Logf("%s: stdlib: %s", label, trimNL(std))
		t.Logf("%s: go-json: %s", label, trimNL(goc))
		return
	}

	t.Fatalf("%s: SEMANTIC DIFFERENCE\nstdlib: %s\ngo-json: %s", label, trimNL(std), trimNL(goc))
}

func trimNL(b []byte) string {
	s := string(b)
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

func TestJSONEncodingVsStdlibFromDB(t *testing.T) {
	if os.Getenv("ISUCON13_JSON_ENCODE_TEST") != "1" {
		t.Skip("set ISUCON13_JSON_ENCODE_TEST=1 to run DB-backed JSON equivalence test")
	}

	db, err := connectDB(echo.New().Logger)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	// User object (GET /api/user/:username shape)
	var userModel UserModel
	if err := tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE id = ?", 1); err != nil {
		t.Fatalf("get user: %v", err)
	}
	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		t.Fatalf("fill user: %v", err)
	}
	compareJSONEncodings(t, "User", user)

	// Livestream list (search / list shape)
	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams ORDER BY id LIMIT 3"); err != nil {
		t.Fatalf("get livestreams: %v", err)
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamModels)
	if err != nil {
		t.Fatalf("fill livestreams: %v", err)
	}
	compareJSONEncodings(t, "[]Livestream", livestreams)

	// Livecomment list with nested user + livestream (pick a stream that has comments)
	var livecommentModels []LivecommentModel
	var livestreamID int64
	if err := tx.GetContext(ctx, &livestreamID,
		"SELECT livestream_id FROM livecomments ORDER BY id DESC LIMIT 1"); err != nil {
		t.Fatalf("find livestream with comments: %v", err)
	}
	if err := tx.SelectContext(ctx, &livecommentModels,
		"SELECT * FROM livecomments WHERE livestream_id = ? ORDER BY id DESC LIMIT 5", livestreamID); err != nil {
		t.Fatalf("get livecomments: %v", err)
	}
	if len(livecommentModels) == 0 {
		t.Fatalf("no livecomments for livestream %d", livestreamID)
	}
	livecomments, err := fillLivecommentsForLivestream(ctx, tx, livecommentModels, livestreamID)
	if err != nil {
		t.Fatalf("fill livecomments: %v", err)
	}
	compareJSONEncodings(t, "[]Livecomment", livecomments)

	// Unicode / Japanese comment text (from seed)
	unicodeComment := livecomments[0]
	for _, lc := range livecomments {
		for _, r := range lc.Comment {
			if r > 127 {
				unicodeComment = lc
				break
			}
		}
	}
	compareJSONEncodings(t, "Livecomment_unicode", unicodeComment)

	// Synthetic emoji overlay on same nested shape
	emojiCopy := unicodeComment
	emojiCopy.Comment = "bench 🎌 テスト " + emojiCopy.Comment
	compareJSONEncodings(t, "Livecomment_emoji", emojiCopy)

	// Tags response (GET /api/tag shape; load via tx to avoid global dbConn in tests)
	var tagModels []*TagModel
	if err := tx.SelectContext(ctx, &tagModels, "SELECT * FROM tags ORDER BY id LIMIT 50"); err != nil {
		t.Fatalf("get tags: %v", err)
	}
	tags := make([]*Tag, len(tagModels))
	for i := range tagModels {
		tags[i] = &Tag{ID: tagModels[i].ID, Name: tagModels[i].Name}
	}
	compareJSONEncodings(t, "TagsResponse", &TagsResponse{Tags: tags})

	// Livecomment report list shape
	var reportModels []*LivecommentReportModel
	if err := tx.SelectContext(ctx, &reportModels,
		"SELECT * FROM livecomment_reports WHERE livestream_id = ? LIMIT 3", livestreamID); err != nil {
		t.Fatalf("get reports: %v", err)
	}
	if len(reportModels) > 0 {
		reports, err := fillLivecommentReportsResponse(ctx, tx, reportModels)
		if err != nil {
			t.Fatalf("fill reports: %v", err)
		}
		compareJSONEncodings(t, "[]LivecommentReport", reports)
	} else {
		t.Log("no livecomment_reports rows; skipping report list encoding compare")
	}

	t.Log("all JSON encoding comparisons finished without semantic differences")
}

// TestJSONEncodingSynthetic runs without DB for quick CI; uses representative nested structs.
func TestJSONEncodingSynthetic(t *testing.T) {
	user := User{
		ID: 1, Name: "test001", DisplayName: "検証用ユーザ", Description: "社内検証用",
		Theme: Theme{ID: 1, DarkMode: true},
		IconHash: "d9f8294e9d895f81ce62e73dc7d5dff862a4fa40bd4e0fecf53f7526a8edcac0",
	}
	ls := Livestream{
		ID: 999, Owner: user, Title: "映画レビュー", Tags: []Tag{{ID: 1, Name: "サプライズ"}},
		Description: "desc", PlaylistUrl: "https://example.com/p.m3u8", ThumbnailUrl: "https://example.com/t.webp",
		StartAt: 1691917200, EndAt: 1691920800,
	}
	lc := Livecomment{
		ID: 1, User: user, Livestream: ls,
		Comment: "あっちの人 🎉 コメント", Tip: 100, CreatedAt: 1691917300,
	}
	compareJSONEncodings(t, "synthetic_User", user)
	compareJSONEncodings(t, "synthetic_Livecomment", lc)
	compareJSONEncodings(t, "synthetic_[]Livecomment", []Livecomment{lc, lc})
}

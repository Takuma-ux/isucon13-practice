package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/labstack/echo/v4"
)

// Pointer-shaped API mirrors (same json tags) for value-vs-pointer encode equivalence.

type livestreamJSON struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	Tags         []Tag   `json:"tags"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type livecommentJSON struct {
	ID         int64           `json:"id"`
	User       User            `json:"user"`
	Livestream livestreamJSON  `json:"livestream"`
	Comment    string           `json:"comment"`
	Tip        int64            `json:"tip"`
	CreatedAt  int64            `json:"created_at"`
}

type reactionJSON struct {
	ID         int64          `json:"id"`
	EmojiName  string         `json:"emoji_name"`
	User       User           `json:"user"`
	Livestream livestreamJSON `json:"livestream"`
	CreatedAt  int64           `json:"created_at"`
}

type livecommentReportJSON struct {
	ID          int64           `json:"id"`
	Reporter    User            `json:"reporter"`
	Livecomment livecommentJSON `json:"livecomment"`
	CreatedAt   int64            `json:"created_at"`
}

func livestreamValueMirror(ls Livestream) livestreamJSON {
	tags := ls.Tags
	if tags == nil {
		tags = []Tag{}
	}
	var owner User
	if ls.Owner != nil {
		owner = *ls.Owner
	}
	return livestreamJSON{
		ID: ls.ID, Owner: owner, Title: ls.Title, Description: ls.Description,
		PlaylistUrl: ls.PlaylistUrl, ThumbnailUrl: ls.ThumbnailUrl, Tags: tags,
		StartAt: ls.StartAt, EndAt: ls.EndAt,
	}
}

func livecommentValueMirror(lc Livecomment) livecommentJSON {
	var u User
	if lc.User != nil {
		u = *lc.User
	}
	var ls livestreamJSON
	if lc.Livestream != nil {
		ls = livestreamValueMirror(*lc.Livestream)
	}
	return livecommentJSON{
		ID: lc.ID, User: u, Livestream: ls,
		Comment: lc.Comment, Tip: lc.Tip, CreatedAt: lc.CreatedAt,
	}
}

func reactionValueMirror(r Reaction) reactionJSON {
	var u User
	if r.User != nil {
		u = *r.User
	}
	var ls livestreamJSON
	if r.Livestream != nil {
		ls = livestreamValueMirror(*r.Livestream)
	}
	return reactionJSON{
		ID: r.ID, EmojiName: r.EmojiName, User: u, Livestream: ls,
		CreatedAt: r.CreatedAt,
	}
}

func reportValueMirror(rep LivecommentReport) livecommentReportJSON {
	var reporter User
	if rep.Reporter != nil {
		reporter = *rep.Reporter
	}
	return livecommentReportJSON{
		ID: rep.ID, Reporter: reporter, Livecomment: livecommentValueMirror(rep.Livecomment),
		CreatedAt: rep.CreatedAt,
	}
}

func assertValuePtrJSONEqual(t *testing.T, label string, valueForm interface{}, ptrForm interface{}) {
	stdVal, err := echoEncodeStdlib(valueForm)
	if err != nil {
		t.Fatalf("%s value stdlib: %v", label, err)
	}
	stdPtr, err := echoEncodeStdlib(ptrForm)
	if err != nil {
		t.Fatalf("%s ptr stdlib: %v", label, err)
	}
	gocVal, err := echoEncodeGoJSON(valueForm)
	if err != nil {
		t.Fatalf("%s value goccy: %v", label, err)
	}
	gocPtr, err := echoEncodeGoJSON(ptrForm)
	if err != nil {
		t.Fatalf("%s ptr goccy: %v", label, err)
	}

	if !bytes.Equal(stdVal, stdPtr) {
		t.Fatalf("%s: stdlib value vs ptr differ\nvalue: %s\nptr:   %s", label, trimNL(stdVal), trimNL(stdPtr))
	}
	if !bytes.Equal(gocVal, gocPtr) {
		t.Fatalf("%s: goccy value vs ptr differ\nvalue: %s\nptr:   %s", label, trimNL(gocVal), trimNL(gocPtr))
	}
	if !bytes.Equal(stdVal, gocVal) {
		t.Fatalf("%s: stdlib vs goccy on value form differ", label)
	}
	t.Logf("%s: byte-identical value/ptr (%d bytes, stdlib=goccy)", label, len(stdVal))
}

func sampleUserFull() User {
	return User{
		ID: 1, Name: "test001", DisplayName: "検証用ユーザ", Description: "社内検証用",
		Theme: Theme{ID: 1, DarkMode: true},
		IconHash: "d9f8294e9d895f81ce62e73dc7d5dff862a4fa40bd4e0fecf53f7526a8edcac0",
	}
}

func sampleUserSparse() User {
	return User{ID: 42, Name: "sparse"}
}

func sampleLivestream(owner User, tags []Tag) Livestream {
	o := owner
	return Livestream{
		ID: 999, Owner: &o, Title: "映画レビュー", Tags: tags,
		Description: "desc", PlaylistUrl: "https://example.com/p.m3u8", ThumbnailUrl: "https://example.com/t.webp",
		StartAt: 1691917200, EndAt: 1691920800,
	}
}

// TestJSONValueVsPointerEmbed confirms production encoders emit identical bytes
// when nested User/Livestream are values vs non-nil pointers to equivalent data.
func TestJSONValueVsPointerEmbed(t *testing.T) {
	full := sampleUserFull()
	sparse := sampleUserSparse()

	cases := []struct {
		label string
		ls    Livestream
	}{
		{"tags_nonempty", sampleLivestream(full, []Tag{{ID: 1, Name: "サプライズ"}})},
		{"tags_empty_slice", sampleLivestream(full, []Tag{})},
		{"owner_sparse", sampleLivestream(sparse, []Tag{})},
	}

	for _, c := range cases {
		lsVal := c.ls
		assertValuePtrJSONEqual(t, "Livestream_"+c.label, livestreamValueMirror(lsVal), lsVal)
	}

	u := full
	ls := sampleLivestream(full, []Tag{{ID: 1, Name: "t"}})
	lc := Livecomment{
		ID: 1, User: &u, Livestream: &ls,
		Comment: "あっちの人 🎉 コメント", Tip: 100, CreatedAt: 1691917300,
	}
	lcMirror := livecommentValueMirror(lc)
	assertValuePtrJSONEqual(t, "Livecomment", lcMirror, lc)
	assertValuePtrJSONEqual(t, "[]Livecomment", []livecommentJSON{lcMirror, lcMirror}, []Livecomment{lc, lc})

	sp := sparse
	lsNilTags := sampleLivestream(full, []Tag{})
	re := Reaction{
		ID: 7, EmojiName: "clap", User: &sp, Livestream: &lsNilTags,
		CreatedAt: 1691917400,
	}
	assertValuePtrJSONEqual(t, "Reaction", reactionValueMirror(re), re)

	repU := full
	rep := LivecommentReport{
		ID: 3, Reporter: &repU, Livecomment: lc, CreatedAt: 1691917500,
	}
	assertValuePtrJSONEqual(t, "LivecommentReport", reportValueMirror(rep), rep)
}

// TestJSONPointerEmbedFilledFromDB checks fill helpers emit the same JSON as pre-pointer value embed.
func TestJSONPointerEmbedFilledFromDB(t *testing.T) {
	if os.Getenv("ISUCON13_JSON_ENCODE_TEST") != "1" {
		t.Skip("set ISUCON13_JSON_ENCODE_TEST=1 to run DB-backed pointer JSON equivalence test")
	}

	db, err := connectDB(echo.New().Logger)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	ctx := withResponseCache(context.Background())
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	var livestreamModels []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreamModels, "SELECT * FROM livestreams ORDER BY id LIMIT 3"); err != nil {
		t.Fatalf("livestreams: %v", err)
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, livestreamModels)
	if err != nil {
		t.Fatalf("fill livestreams: %v", err)
	}
	for _, ls := range livestreams {
		assertValuePtrJSONEqual(t, "filled_Livestream", livestreamValueMirror(ls), ls)
	}
	assertValuePtrJSONEqual(t, "filled_[]Livestream", livestreamsToValueMirror(livestreams), livestreams)

	var livestreamID int64
	if err := tx.GetContext(ctx, &livestreamID, "SELECT livestream_id FROM livecomments ORDER BY id DESC LIMIT 1"); err != nil {
		t.Fatalf("livestream id: %v", err)
	}
	var livecommentModels []LivecommentModel
	if err := tx.SelectContext(ctx, &livecommentModels, "SELECT * FROM livecomments WHERE livestream_id = ? ORDER BY id DESC LIMIT 5", livestreamID); err != nil {
		t.Fatalf("livecomments: %v", err)
	}
	if len(livecommentModels) == 0 {
		t.Fatal("no livecomments")
	}
	livecomments, err := fillLivecommentsForLivestream(ctx, tx, livecommentModels, livestreamID)
	if err != nil {
		t.Fatalf("fill livecomments: %v", err)
	}
	for _, lc := range livecomments {
		assertValuePtrJSONEqual(t, "filled_Livecomment", livecommentValueMirror(lc), lc)
	}
}

func livestreamsToValueMirror(livestreams []Livestream) []livestreamJSON {
	out := make([]livestreamJSON, len(livestreams))
	for i, ls := range livestreams {
		out[i] = livestreamValueMirror(ls)
	}
	return out
}

// TestJSONPointerEmbedNilFields documents nil-pointer JSON (not used in API fills).
func TestJSONPointerEmbedNilFields(t *testing.T) {
	lcNil := livecommentJSON{ID: 1, Comment: "x", Tip: 0, CreatedAt: 1}
	b, err := echoEncodeGoJSON(lcNil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"user":null`)) {
		t.Logf("nil *User encodes as null: %s", trimNL(b))
	}
	lcVal := Livecomment{ID: 1, Comment: "x", CreatedAt: 1, User: &User{}, Livestream: &Livestream{Tags: []Tag{}}}
	bVal, _ := echoEncodeGoJSON(lcVal)
	if bytes.Equal(b, bVal) {
		t.Fatal("expected nil pointer nested fields to differ from zero-value-shaped non-nil pointers")
	}
	t.Log("API fills must use non-nil User/Livestream/TAGS; nil Tags encodes as null not []")
}

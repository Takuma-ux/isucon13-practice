package main

import (
	"bytes"
	"testing"

	gojson "github.com/goccy/go-json"
)

// Mirrors API list responses: N elements sharing the same nested livestream object.
type livecommentSharedPtr []Livecomment

type livecommentRawLivestream struct {
	ID         int64           `json:"id"`
	User       *User           `json:"user"`
	Livestream gojson.RawMessage `json:"livestream"`
	Comment    string          `json:"comment"`
	Tip        int64           `json:"tip"`
	CreatedAt  int64           `json:"created_at"`
}

type livecommentRawPtr []livecommentRawLivestream

func benchLivestream() *Livestream {
	owner := &User{
		ID: 1, Name: "streamer001", DisplayName: "Streamer",
		Description: "desc", IconHash: "d9f8294e9d895f81ce62e73dc7d5dff862a4fa40bd4e0fecf53f7526a8edcac0",
		Theme: Theme{ID: 1, DarkMode: true},
	}
	tags := make([]Tag, 8)
	for i := range tags {
		tags[i] = Tag{ID: int64(i + 1), Name: "tag-name"}
	}
	return &Livestream{
		ID: 42, Owner: owner, Title: "bench livestream",
		Description: "long description text for realistic payload size",
		PlaylistUrl: "https://media.xiii.isucon.dev/api/1/playlist.m3u8",
		ThumbnailUrl: "https://media.xiii.isucon.dev/api/1/thumbnail.webp",
		Tags: tags, StartAt: 1700000000, EndAt: 1700003600,
	}
}

func benchUser() *User {
	return &User{
		ID: 99, Name: "viewer042", DisplayName: "Viewer",
		Description: "viewer profile", IconHash: "abc123",
		Theme: Theme{ID: 2, DarkMode: false},
	}
}

func makeSharedPtrComments(n int) livecommentSharedPtr {
	ls := benchLivestream()
	u := benchUser()
	out := make(livecommentSharedPtr, n)
	for i := range out {
		out[i] = Livecomment{
			ID: int64(i + 1), User: u, Livestream: ls,
			Comment: "comment body for bench", Tip: int64(i % 5), CreatedAt: 1700000100 + int64(i),
		}
	}
	return out
}

func makeRawMessageComments(n int, lsJSON gojson.RawMessage) livecommentRawPtr {
	u := benchUser()
	out := make(livecommentRawPtr, n)
	for i := range out {
		out[i] = livecommentRawLivestream{
			ID: int64(i + 1), User: u, Livestream: lsJSON,
			Comment: "comment body for bench", Tip: int64(i % 5), CreatedAt: 1700000100 + int64(i),
		}
	}
	return out
}

func TestNestedLivestreamEncodeBytesMatch(t *testing.T) {
	const n = 20
	shared := makeSharedPtrComments(n)
	std, err := gojson.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	ls := benchLivestream()
	lsJSON, err := gojson.Marshal(ls)
	if err != nil {
		t.Fatal(err)
	}
	raw := makeRawMessageComments(n, lsJSON)
	opt, err := gojson.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(std, opt) {
		t.Fatalf("encoded JSON differs: std %d bytes raw %d bytes", len(std), len(opt))
	}
}

func BenchmarkEncodeLivecommentsSharedPtr20(b *testing.B) {
	payload := makeSharedPtrComments(20)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gojson.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeLivecommentsRawMessage20(b *testing.B) {
	lsJSON, err := gojson.Marshal(benchLivestream())
	if err != nil {
		b.Fatal(err)
	}
	payload := makeRawMessageComments(20, lsJSON)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gojson.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeLivecommentsSharedPtr50(b *testing.B) {
	payload := makeSharedPtrComments(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gojson.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeLivecommentsRawMessage50(b *testing.B) {
	lsJSON, err := gojson.Marshal(benchLivestream())
	if err != nil {
		b.Fatal(err)
	}
	payload := makeRawMessageComments(50, lsJSON)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gojson.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}

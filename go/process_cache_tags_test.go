package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAssembleLivestreamFromProcessCache_zeroTagsNonNil(t *testing.T) {
	const livestreamID = 4242
	const ownerID = 77

	cases := []struct {
		name string
		tags []Tag
	}{
		{"nil_tags_slice", nil},
		{"empty_tags_slice", []Tag{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invalidateProcessReadCaches()
			writeThroughUserProfileCache(&User{
				ID:       ownerID,
				Name:     "owner",
				Theme:    Theme{ID: 1},
				IconHash: "hash",
			})
			putLivestreamShellCache(&LivestreamShell{
				ID:           livestreamID,
				OwnerUserID:  ownerID,
				Title:        "no tags stream",
				Description:  "",
				PlaylistUrl:  "https://example.invalid/playlist",
				ThumbnailUrl: "https://example.invalid/thumb",
				Tags:         tc.tags,
				StartAt:      1,
				EndAt:        2,
				tagsResolved: true,
			})

			ls, ok := assembleLivestreamFromProcessCache(context.Background(), livestreamID, nil)
			if !ok {
				t.Fatal("assembleLivestreamFromProcessCache: expected hit")
			}
			if ls == nil {
				t.Fatal("nil livestream")
			}
			if ls.Tags == nil {
				t.Fatal("Tags must be non-nil empty slice, not nil (JSON null)")
			}
			if len(ls.Tags) != 0 {
				t.Fatalf("expected 0 tags, got %d", len(ls.Tags))
			}

			var payload struct {
				Tags json.RawMessage `json:"tags"`
			}
			if err := json.Unmarshal(mustJSON(t, ls), &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(payload.Tags) != "[]" {
				t.Fatalf("tags JSON: want [], got %s", payload.Tags)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAssembledLivestreamCacheJSONMatchesFreshAssembly(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()

	var lsID int64
	if err := dbConn.GetContext(ctx, &lsID, "SELECT livestream_id FROM livestream_tags ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("pick livestream: %v", err)
	}

	var model LivestreamModel
	if err := dbConn.GetContext(ctx, &model, "SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams WHERE id = ?", lsID); err != nil {
		t.Fatalf("livestream row: %v", err)
	}

	invalidateProcessReadCaches()
	writeThroughUserProfileCache(&User{
		ID:          model.UserID,
		Name:        "assembled-cache-owner",
		DisplayName: "display",
		Description: "desc",
		Theme:       Theme{ID: 1, DarkMode: false},
		IconHash:    "before_icon_hash",
	})

	filled, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{&model})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	wantJSON := mustJSON(t, filled[0])

	clearAssembledLivestreamCache()

	fresh, ok := assembleLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("fresh assemble: expected hit")
	}
	if string(mustJSON(t, fresh)) != string(wantJSON) {
		t.Fatalf("fresh assemble JSON mismatch fill baseline")
	}

	cached1, ok := getAssembledLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("getAssembled first: expected hit")
	}
	cached2, ok := getAssembledLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("getAssembled second: expected hit")
	}
	if string(mustJSON(t, cached1)) != string(wantJSON) {
		t.Fatalf("cached1 JSON mismatch")
	}
	if string(mustJSON(t, cached2)) != string(wantJSON) {
		t.Fatalf("cached2 JSON mismatch")
	}
}

func TestAssembledLivestreamCacheInvalidatesOnIconWriteThrough(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()

	var lsID int64
	if err := dbConn.GetContext(ctx, &lsID, "SELECT livestream_id FROM livestream_tags ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("pick livestream: %v", err)
	}

	var model LivestreamModel
	if err := dbConn.GetContext(ctx, &model, "SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams WHERE id = ?", lsID); err != nil {
		t.Fatalf("livestream row: %v", err)
	}

	invalidateProcessReadCaches()
	writeThroughUserProfileCache(&User{
		ID:          model.UserID,
		Name:        "icon-invalidate-owner",
		DisplayName: "display",
		Description: "desc",
		Theme:       Theme{ID: 1},
		IconHash:    "hash_before_upload",
	})
	_, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{&model})
	if err != nil {
		t.Fatalf("fill: %v", err)
	}

	before, ok := getAssembledLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("expected assembled cache hit before icon write-through")
	}
	beforeJSON := mustJSON(t, before)

	writeThroughUserProfileCache(&User{
		ID:          model.UserID,
		Name:        "icon-invalidate-owner",
		DisplayName: "display",
		Description: "desc",
		Theme:       Theme{ID: 1},
		IconHash:    "hash_after_upload",
	})

	afterCached, ok := getAssembledLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("expected assembled cache hit after icon write-through")
	}
	afterFresh, ok := assembleLivestreamFromProcessCache(ctx, lsID, nil)
	if !ok {
		t.Fatal("expected fresh assemble after icon write-through")
	}

	afterCachedJSON := mustJSON(t, afterCached)
	afterFreshJSON := mustJSON(t, afterFresh)
	if string(afterCachedJSON) == string(beforeJSON) {
		t.Fatal("owner icon_hash in assembled cache should change after writeThroughUserProfileCache")
	}
	if string(afterCachedJSON) != string(afterFreshJSON) {
		t.Fatalf("cached vs fresh JSON after icon invalidation\ncached: %s\nfresh:  %s", afterCachedJSON, afterFreshJSON)
	}

	var ownerField struct {
		Owner struct {
			IconHash string `json:"icon_hash"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(afterCachedJSON, &ownerField); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ownerField.Owner.IconHash != "hash_after_upload" {
		t.Fatalf("icon_hash: got %q want hash_after_upload", ownerField.Owner.IconHash)
	}
}

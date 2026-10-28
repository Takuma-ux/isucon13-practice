package main

import (
	"context"
	"encoding/json"
	"testing"
)

// Shell tags from req.Tags must produce the same Livestream JSON as tagsByLivestreamID after DB insert.
func TestReserveShellTagsMatchDBFillJSON(t *testing.T) {
	requireSharedIntegrationDB(t)
	ctx := context.Background()

	var lsID int64
	if err := dbConn.GetContext(ctx, &lsID, "SELECT livestream_id FROM livestream_tags ORDER BY id LIMIT 1"); err != nil {
		t.Fatalf("pick livestream with tags: %v", err)
	}

	var model LivestreamModel
	if err := dbConn.GetContext(ctx, &model, "SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams WHERE id = ?", lsID); err != nil {
		t.Fatalf("livestream row: %v", err)
	}

	var tagIDs []int64
	if err := dbConn.SelectContext(ctx, &tagIDs, "SELECT tag_id FROM livestream_tags WHERE livestream_id = ? ORDER BY id", lsID); err != nil {
		t.Fatalf("tag ids: %v", err)
	}

	invalidateProcessReadCaches()
	writeThroughUserProfileCache(&User{
		ID:       model.UserID,
		Name:     "shell-tags-test-owner",
		Theme:    Theme{ID: 1},
		IconHash: defaultIconHash,
	})

	fromDB, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{&model})
	if err != nil {
		t.Fatalf("fill from DB: %v", err)
	}

	invalidateProcessReadCaches()
	writeThroughUserProfileCache(&User{
		ID:       model.UserID,
		Name:     "shell-tags-test-owner",
		Theme:    Theme{ID: 1},
		IconHash: defaultIconHash,
	})
	shellTags, err := tagsFromRequestTagIDs(ctx, tagIDs)
	if err != nil {
		t.Fatalf("tagsFromRequestTagIDs: %v", err)
	}
	putLivestreamShellCache(livestreamShellFromModel(&model, shellTags, true))

	fromShell, err := fillLivestreamsResponse(ctx, dbConn, []*LivestreamModel{&model})
	if err != nil {
		t.Fatalf("fill from shell: %v", err)
	}

	bDB, err := json.Marshal(fromDB[0])
	if err != nil {
		t.Fatalf("marshal db: %v", err)
	}
	bShell, err := json.Marshal(fromShell[0])
	if err != nil {
		t.Fatalf("marshal shell: %v", err)
	}
	if string(bDB) != string(bShell) {
		t.Fatalf("livestream JSON mismatch\ndb:    %s\nshell: %s", bDB, bShell)
	}
}

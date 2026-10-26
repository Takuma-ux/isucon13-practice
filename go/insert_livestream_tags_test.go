package main

import (
	"context"
	"testing"
	"time"
)

func TestInsertLivestreamTagsFiveInOneStatement(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	db := requireSharedIntegrationDB(t)

	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	lsID := time.Now().UnixNano() % 900000000
	tagIDs := []int64{1, 2, 3, 4, 5}

	t0 := time.Now()
	if err := insertLivestreamTags(ctx, tx, lsID, tagIDs); err != nil {
		t.Fatalf("insert: %v", err)
	}
	elapsed := time.Since(t0)

	var cnt int
	if err := tx.GetContext(ctx, &cnt, "SELECT COUNT(*) FROM livestream_tags WHERE livestream_id = ?", lsID); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 5 {
		t.Fatalf("expected 5 rows, got %d", cnt)
	}
	t.Logf("insertLivestreamTags(5): 1 ExecContext, %v", elapsed)
}

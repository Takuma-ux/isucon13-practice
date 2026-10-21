package main

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

func TestSqlxInEmptyTagIDList(t *testing.T) {
	var tagIDList []int
	_, _, err := sqlx.In("SELECT * FROM livestream_tags WHERE tag_id IN (?)", tagIDList)
	if err == nil {
		t.Fatal("expected error for empty IN list")
	}
}

func TestLookupTagIDsByNameMatchesDB(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	db, err := connectDB(echo.New().Logger)
	if err != nil {
		t.Skipf("db unavailable: %v", err)
	}
	defer db.Close()

	prev := dbConn
	dbConn = db
	defer func() {
		dbConn = prev
		invalidateTagsCache()
	}()
	invalidateTagsCache()

	ctx := context.Background()
	const tagName = "椅子"

	var fromDB []int
	if err := db.SelectContext(ctx, &fromDB, "SELECT id FROM tags WHERE name = ?", tagName); err != nil {
		t.Fatalf("select tags: %v", err)
	}
	fromCache, err := lookupTagIDsByName(ctx, tagName)
	if err != nil {
		t.Fatalf("lookupTagIDsByName: %v", err)
	}
	if len(fromCache) != len(fromDB) {
		t.Fatalf("id count: cache=%d db=%d", len(fromCache), len(fromDB))
	}
	for i := range fromDB {
		if fromCache[i] != fromDB[i] {
			t.Fatalf("id[%d]: cache=%d db=%d", i, fromCache[i], fromDB[i])
		}
	}

	unknown, err := lookupTagIDsByName(ctx, "__no_such_tag__")
	if err != nil {
		t.Fatalf("unknown tag lookup: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("expected empty ids for unknown tag, got %v", unknown)
	}
}

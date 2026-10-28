package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Locks in warmTagsCache on POST /api/initialize (tags must not stay cold until first GET /api/tag).
func TestInitializeWarmsTagsCacheHTTP(t *testing.T) {
	tagHandlerTestDB(t)
	ctx := context.Background()
	prependFakePdnsutilToPATH(t)

	var dbTagCount int
	if err := dbConn.GetContext(ctx, &dbTagCount, "SELECT COUNT(*) FROM tags"); err != nil {
		t.Fatalf("count tags: %v", err)
	}
	if dbTagCount == 0 {
		t.Skip("database has no tags — load sql/init.sh seed data first")
	}

	var dbTags []TagModel
	if err := dbConn.SelectContext(ctx, &dbTags, "SELECT id, name FROM tags ORDER BY id"); err != nil {
		t.Fatalf("select tags: %v", err)
	}

	invalidateTagsCache()
	tagsCacheMu.RLock()
	if tagsCache != nil {
		tagsCacheMu.RUnlock()
		t.Fatal("expected nil tags cache before initialize")
	}
	tagsCacheMu.RUnlock()

	e := echo.New()
	e.POST("/api/initialize", initializeHandler)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/initialize", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/initialize status %d body=%s", rec.Code, rec.Body.String())
	}

	tagsCacheMu.RLock()
	cached := tagsCache
	byID := tagsByIDCache
	tagsCacheMu.RUnlock()

	if cached == nil {
		t.Fatal("tagsCache must be non-nil immediately after POST /api/initialize (warmTagsCache in handler)")
	}
	if len(cached.Tags) != dbTagCount {
		t.Fatalf("tagsCache tag count: got %d want %d", len(cached.Tags), dbTagCount)
	}
	if byID == nil {
		t.Fatal("tagsByIDCache must be non-nil immediately after POST /api/initialize")
	}
	if len(byID) != dbTagCount {
		t.Fatalf("tagsByIDCache size: got %d want %d", len(byID), dbTagCount)
	}
	for _, row := range dbTags {
		tag, ok := byID[row.ID]
		if !ok {
			t.Fatalf("tagsByIDCache missing tag id %d", row.ID)
		}
		if tag.Name != row.Name {
			t.Fatalf("tag id %d: cache name %q want %q", row.ID, tag.Name, row.Name)
		}
	}
}

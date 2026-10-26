package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func tagHandlerTestDB(t *testing.T) {
	sharedIntegrationDBOrSkip(t)
}

// Proves tag cache repopulates via getCachedTags after invalidate (initialize path).
func TestTagsCacheLazyRepopulationAfterInvalidate(t *testing.T) {
	tagHandlerTestDB(t)
	ctx := context.Background()

	var tagCount int
	if err := dbConn.GetContext(ctx, &tagCount, "SELECT COUNT(*) FROM tags"); err != nil {
		t.Fatalf("count tags: %v", err)
	}
	if tagCount == 0 {
		t.Skip("database has no tags — load sql/init.sh seed data first")
	}

	if err := warmTagsCache(ctx); err != nil {
		t.Fatalf("warm: %v", err)
	}
	tagsCacheMu.RLock()
	warmed := tagsCache
	tagsCacheMu.RUnlock()
	if warmed == nil || len(warmed.Tags) != tagCount {
		t.Fatalf("warmed cache: got %d tags want %d", len(warmed.Tags), tagCount)
	}

	invalidateTagsCache()
	tagsCacheMu.RLock()
	if tagsCache != nil {
		tagsCacheMu.RUnlock()
		t.Fatal("cache should be nil immediately after invalidate")
	}
	tagsCacheMu.RUnlock()

	reloaded, err := getCachedTags(ctx)
	if err != nil {
		t.Fatalf("getCachedTags after invalidate: %v", err)
	}
	if len(reloaded.Tags) != len(warmed.Tags) {
		t.Fatalf("tag count after lazy reload: got %d want %d", len(reloaded.Tags), len(warmed.Tags))
	}

	e := echo.New()
	e.GET("/api/tag", getTagHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tag", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tag status %d body=%s", rec.Code, rec.Body.String())
	}
	var body TagsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tags) != len(warmed.Tags) {
		t.Fatalf("handler tag count: got %d want %d", len(body.Tags), len(warmed.Tags))
	}
}

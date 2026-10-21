package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestSearchLivestreamsByTagHTTP(t *testing.T) {
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

	e := echo.New()
	e.GET("/api/livestream/search", searchLivestreamsHandler)

	t.Run("known_tag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/livestream/search?tag="+url.QueryEscape("椅子"), nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out []Livestream
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out) == 0 {
			t.Fatal("expected at least one livestream for tag 椅子")
		}
	})

	t.Run("unknown_tag", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/livestream/search?tag="+url.QueryEscape("__no_such_tag__"), nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out []Livestream
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("expected empty list, got %d", len(out))
		}
	})
}

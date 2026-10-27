package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// Proves POST /api/register makes GET statistics work for the new username without rebuildStatsFromDB.
func TestRegisterHandlerAddsUserToStatsMaps(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sharedIntegrationDBOrSkip(t)
	ctx := context.Background()
	if err := rebuildStatsFromDB(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	prependFakePdnsutilToPATH(t)
	powerDNSSubdomainAddress = "127.0.0.1"

	username := fmt.Sprintf("statsreg%d", time.Now().UnixNano())
	reqBody, err := json.Marshal(&PostUserRequest{
		Name:        username,
		DisplayName: "stats register test",
		Description: "bench 400 repro",
		Password:    "testpass",
		Theme:       PostUserRequestTheme{DarkMode: false},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.POST("/api/register", registerHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status %d body=%s", rec.Code, rec.Body.String())
	}

	_, err = userStatisticsFromMemory(username)
	if errors.Is(err, errUserStatsNotFound) {
		t.Fatalf("user %q missing from stats maps after register (GET statistics would return 400)", username)
	}
	if err != nil {
		t.Fatalf("userStatisticsFromMemory: %v", err)
	}
}

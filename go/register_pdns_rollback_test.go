package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func prependFailingPdnsutilToPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pdnsutil")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing pdnsutil: %v", err)
	}
	prev := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+prev)
	t.Setenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS", "127.0.0.1")
}

func TestRegisterPdnsFailureRemovesUserAndTheme(t *testing.T) {
	db := testDB(t)
	dbConn = db
	powerDNSSubdomainAddress = "127.0.0.1"
	prependFailingPdnsutilToPATH(t)

	ctx := context.Background()
	username := fmt.Sprintf("pdnsfail%d", time.Now().UnixNano())
	reqBody, err := json.Marshal(&PostUserRequest{
		Name:        username,
		DisplayName: "pdns fail test",
		Description: "rollback",
		Password:    "test",
		Theme:       PostUserRequestTheme{DarkMode: false},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	e := newTestEcho()
	e.POST("/api/register", registerHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on pdns failure, got %d body=%s", rec.Code, rec.Body.String())
	}

	var userCount int
	if err := dbConn.GetContext(ctx, &userCount, `SELECT COUNT(*) FROM users WHERE name = ?`, username); err != nil {
		t.Fatalf("count users: %v", err)
	}
	var themeCount int
	if err := dbConn.GetContext(ctx, &themeCount, `
		SELECT COUNT(*) FROM themes t
		INNER JOIN users u ON u.id = t.user_id
		WHERE u.name = ?`, username); err != nil {
		t.Fatalf("count themes: %v", err)
	}
	if userCount != 0 || themeCount != 0 {
		t.Fatalf("compensating delete failed: users=%d themes=%d for name %q", userCount, themeCount, username)
	}
}

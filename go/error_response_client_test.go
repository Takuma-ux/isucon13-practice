package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestSanitizeServerErrorMessage(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"failed to begin transaction: dial tcp 127.0.0.1:3306: connect: connection refused", "failed to begin transaction"},
		{"failed to insert livecomment: Error 1213: Deadlock found", "failed to insert livecomment"},
		{"failed to delete old livecomments that hit spams: context deadline exceeded", "failed to delete old livecomments that hit spams"},
		{"failed to get NG words: internal server error", "failed to get NG words"},
		{"dial tcp: connection refused", "internal server error"},
		{"boom", "boom"},
	}
	for _, tc := range cases {
		got := sanitizeServerErrorMessage(tc.in)
		if got != tc.want {
			t.Fatalf("sanitize(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrorResponseHandlerSanitizes500BodyButLogsFullDetail(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	var logBuf bytes.Buffer
	e.Logger.SetOutput(&logBuf)

	internal := "failed to begin transaction: dial tcp 127.0.0.1:3306: connect: connection refused"
	e.GET("/db", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusInternalServerError, internal)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/db", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(er.Error, "dial tcp") {
		t.Fatalf("response leaked internal detail: %q", er.Error)
	}
	if er.Error != "code=500, message=failed to begin transaction" {
		t.Fatalf("unexpected client message: %q", er.Error)
	}
	if !strings.Contains(logBuf.String(), "dial tcp") {
		t.Fatalf("server log missing full error: %q", logBuf.String())
	}
}

func TestErrorResponseHandlerPreserves4xxMessage(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.GET("/bad", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusBadRequest, "nope")
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bad", nil))
	var er ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if er.Error != "code=400, message=nope" {
		t.Fatalf("got %q", er.Error)
	}
}

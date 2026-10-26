package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestErrorResponseHandlerStatusCodes(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.GET("/bad", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusBadRequest, "nope")
	})
	e.GET("/fail", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusInternalServerError, "boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/fail", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("fail: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "code=500, message=boom") {
		t.Fatalf("fail: unexpected body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "dial tcp") {
		t.Fatalf("fail: body should not contain driver text")
	}
}

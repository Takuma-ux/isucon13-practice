package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func TestRecoverMiddlewareReturns500OnPanic(t *testing.T) {
	e := echo.New()
	e.Use(middleware.Recover())
	e.GET("/panic", func(c echo.Context) error {
		panic("deliberate test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%q", rec.Code, rec.Body.String())
	}
}

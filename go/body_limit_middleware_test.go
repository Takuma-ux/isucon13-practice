package main

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Official bench IconSched: largest POST /api/icon JSON body is 228884 bytes (56.jpg, base64 in JSON).
const benchMaxIconJSONBodyBytes = 228884

func TestBodyLimit_AllowsBenchMaxIconPayload(t *testing.T) {
	body := largestBenchIconJSONBody(t)
	if len(body) > 700*1024 {
		t.Fatalf("bench icon JSON %d bytes exceeds configured limit 700K", len(body))
	}

	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.Use(middleware.BodyLimit(httpRequestBodyLimit))
	var handled bool
	e.POST("/api/icon", func(c echo.Context) error {
		handled = true
		var v struct {
			Image []byte `json:"image"`
		}
		if err := decodeRequestBody(c, &v); err != nil {
			return err
		}
		if len(v.Image) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "empty image")
		}
		return c.NoContent(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/icon", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("bench-sized icon JSON rejected with 413 (len=%d)", len(body))
	}
	if !handled {
		t.Fatalf("handler not reached, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestBodyLimit_RejectsOversizedPayloadWith413(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.Use(middleware.BodyLimit(httpRequestBodyLimit))
	var handled bool
	e.POST("/api/icon", func(c echo.Context) error {
		handled = true
		return c.NoContent(http.StatusOK)
	})

	oversized := bytes.Repeat([]byte("x"), 700*1024+1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/icon", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)

	if handled {
		t.Fatal("handler should not run when body exceeds limit")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func largestBenchIconJSONBody(t *testing.T) []byte {
	candidates := []string{
		"/tmp/isucon13-bench-ng/bench/internal/scheduler/images/56.jpg",
		filepath.Join("..", "..", "isucon13", "bench", "internal", "scheduler", "images", "56.jpg"),
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(raw)
		body := []byte(`{"image":"` + b64 + `"}`)
		t.Logf("using bench icon fixture %s raw=%d json=%d", p, len(raw), len(body))
		return body
	}
	t.Logf("bench 56.jpg not found; using synthetic body %d bytes", benchMaxIconJSONBodyBytes)
	return bytes.Repeat([]byte("A"), benchMaxIconJSONBodyBytes)
}

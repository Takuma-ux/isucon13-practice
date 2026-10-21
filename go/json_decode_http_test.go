package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

const malformedJSONBody = "{not-valid-json"
// echo.HTTPError.Error() is what errorResponseHandler puts in JSON "error" (unchanged from stdlib decode path).
const wantDecodeErrorMsg = "code=400, message=failed to decode the request body as json"

func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler
	e.Use(session.Middleware(sessions.NewCookieStore(secret)))
	return e
}

func assertBadJSONResponse(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var er ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("response JSON: %v raw=%q", err, rec.Body.String())
	}
	if er.Error != wantDecodeErrorMsg {
		t.Fatalf("error message: got %q want %q", er.Error, wantDecodeErrorMsg)
	}
	t.Logf("HTTP %d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
}

func sessionCookieForUser(t *testing.T, e *echo.Echo, userID int64) string {
	t.Helper()
	e.GET("/_test_session_bootstrap", func(c echo.Context) error {
		sess, err := session.Get(defaultSessionIDKey, c)
		if err != nil {
			return err
		}
		sess.Values[defaultUserIDKey] = userID
		sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
		if err := sess.Save(c.Request(), c.Response()); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/_test_session_bootstrap", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap session: status %d", rec.Code)
	}
	resp := rec.Result()
	defer resp.Body.Close()
	if len(resp.Cookies()) == 0 {
		t.Fatal("no session cookie")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range resp.Cookies() {
		req2.AddCookie(c)
	}
	return req2.Header.Get("Cookie")
}

func postMalformed(t *testing.T, e *echo.Echo, path string, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(malformedJSONBody))
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestMalformedJSONHTTP_Login(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/login", loginHandler)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/login", ""))
}

func TestMalformedJSONHTTP_Register(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/register", registerHandler)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/register", ""))
}

func TestMalformedJSONHTTP_PostIcon(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/icon", postIconHandler)
	cookie := sessionCookieForUser(t, e, 1)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/icon", cookie))
}

func TestMalformedJSONHTTP_Reserve(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	cookie := sessionCookieForUser(t, e, 1)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/livestream/reservation", cookie))
}

func TestMalformedJSONHTTP_PostLivecomment(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	cookie := sessionCookieForUser(t, e, 1)
	rec := postMalformed(t, e, "/api/livestream/1/livecomment", cookie)
	assertBadJSONResponse(t, rec)
}

func TestMalformedJSONHTTP_PostReaction(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	cookie := sessionCookieForUser(t, e, 1)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/livestream/1/reaction", cookie))
}

func TestMalformedJSONHTTP_Moderate(t *testing.T) {
	e := newTestEcho()
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)
	cookie := sessionCookieForUser(t, e, 1)
	assertBadJSONResponse(t, postMalformed(t, e, "/api/livestream/1/moderate", cookie))
}

// Live curl against a running server (optional): ISUCON13_HTTP_E2E=1 ISUCON13_E2E_BASE=http://127.0.0.1:8080
func TestMalformedJSONHTTP_LiveServerOptional(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("ISUCON13_E2E_BASE")), "/")
	if os.Getenv("ISUCON13_HTTP_E2E") != "1" || base == "" {
		t.Skip("set ISUCON13_HTTP_E2E=1 and ISUCON13_E2E_BASE=http://host:port to run live HTTP checks")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/api/login", "/api/register"} {
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(malformedJSONBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json;charset=utf-8")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Logf("LIVE %s -> %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		var er ErrorResponse
		if err := json.Unmarshal(body, &er); err != nil {
			t.Fatalf("%s: json: %v", path, err)
		}
		if er.Error != wantDecodeErrorMsg {
			t.Fatalf("%s: message %q", path, er.Error)
		}
	}
}

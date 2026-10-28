package main

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// parsePositiveLimitQuery parses ?limit= when present. Official reference accepts any integer
// in LIMIT; we reject non-positive values (bench only sends positive limits).
func parsePositiveLimitQuery(c echo.Context) (limit int, hasLimit bool, httpErr *echo.HTTPError) {
	s := c.QueryParam("limit")
	if s == "" {
		return 0, false, nil
	}
	limit, err := strconv.Atoi(s)
	if err != nil {
		return 0, true, echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
	}
	if limit <= 0 {
		return 0, true, echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be positive")
	}
	return limit, true, nil
}

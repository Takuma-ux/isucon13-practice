package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

// mysql/driver and OS errors spliced into handler messages via err.Error().
var internalErrorDetailRE = regexp.MustCompile(`(?i)(dial tcp|connection refused|invalid connection|access denied|syscall|timeout|broken pipe|driver:|mysql|sql:|Error [0-9]{4})`)

func httpErrorMessage(he *echo.HTTPError) string {
	if he.Message == nil {
		return ""
	}
	switch m := he.Message.(type) {
	case string:
		return m
	case error:
		return m.Error()
	default:
		return fmt.Sprint(m)
	}
}

func clientFacingHTTPErrorString(he *echo.HTTPError) string {
	msg := httpErrorMessage(he)
	if he.Code < http.StatusInternalServerError {
		return he.Error()
	}
	safe := sanitizeServerErrorMessage(msg)
	return fmt.Sprintf("code=%d, message=%s", he.Code, safe)
}

func sanitizeServerErrorMessage(msg string) string {
	if msg == "" {
		return "internal server error"
	}
	if internalErrorDetailRE.MatchString(msg) && !strings.HasPrefix(msg, "failed to ") {
		return "internal server error"
	}
	const sep = ": "
	if i := strings.Index(msg, sep); i >= 0 {
		prefix, suffix := msg[:i], msg[i+len(sep):]
		if strings.HasPrefix(prefix, "failed to ") {
			return prefix
		}
		if internalErrorDetailRE.MatchString(suffix) && prefix != "" {
			return prefix
		}
	}
	return msg
}

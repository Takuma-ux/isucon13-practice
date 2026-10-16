package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	gojson "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
)

// GoJSONSerializer implements Echo JSON encoding with goccy/go-json (responses only).
// Deserialize still uses encoding/json so handler json.NewDecoder behavior is unchanged.
type GoJSONSerializer struct{}

func (d GoJSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	enc := gojson.NewEncoder(c.Response())
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	return enc.Encode(i)
}

func (d GoJSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	err := json.NewDecoder(c.Request().Body).Decode(i)
	if ute, ok := err.(*json.UnmarshalTypeError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
	} else if se, ok := err.(*json.SyntaxError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
	}
	return err
}

package main

import (
	"fmt"
	"io"
	"net/http"

	gojson "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
)

// GoJSONSerializer implements Echo JSON encoding/decoding with goccy/go-json.
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
	if err := decodeRequestJSON(c.Request().Body, i); err != nil {
		return mapJSONDecodeError(err)
	}
	return nil
}

// decodeRequestJSON decodes a JSON request body (stdlib-equivalent for app request shapes; see json_decode_compare_test.go).
func decodeRequestJSON(body io.Reader, v interface{}) error {
	return gojson.NewDecoder(body).Decode(v)
}

func decodeRequestBody(c echo.Context, v interface{}) error {
	if err := decodeRequestJSON(c.Request().Body, v); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}
	return nil
}

func mapJSONDecodeError(err error) error {
	if ute, ok := err.(*gojson.UnmarshalTypeError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
	}
	if se, ok := err.(*gojson.SyntaxError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
	}
	return err
}

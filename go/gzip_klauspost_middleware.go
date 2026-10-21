package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/labstack/echo/v4"
)

// klauspostGzipConfig mirrors Echo's GzipConfig for a drop-in faster compressor.
type klauspostGzipConfig struct {
	Skipper   func(c echo.Context) bool
	Level     int
	MinLength int
}

var defaultKlauspostGzipConfig = klauspostGzipConfig{
	Level:     gzip.DefaultCompression,
	MinLength: 0,
}

func klauspostGzipMiddleware(config klauspostGzipConfig) echo.MiddlewareFunc {
	if config.Skipper == nil {
		config.Skipper = func(c echo.Context) bool { return false }
	}
	if config.Level == 0 {
		config.Level = defaultKlauspostGzipConfig.Level
	}
	if config.MinLength < 0 {
		config.MinLength = 0
	}

	pool := sync.Pool{
		New: func() interface{} {
			w, err := gzip.NewWriterLevel(io.Discard, config.Level)
			if err != nil {
				return err
			}
			return w
		},
	}
	bpool := sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if config.Skipper(c) {
				return next(c)
			}

			res := c.Response()
			res.Header().Add(echo.HeaderVary, echo.HeaderAcceptEncoding)
			if !strings.Contains(c.Request().Header.Get(echo.HeaderAcceptEncoding), "gzip") {
				return next(c)
			}

			i := pool.Get()
			w, ok := i.(*gzip.Writer)
			if !ok {
				return echo.NewHTTPError(http.StatusInternalServerError, i.(error).Error())
			}
			rw := res.Writer
			w.Reset(rw)

			buf := bpool.Get().(*bytes.Buffer)
			buf.Reset()

			grw := &klauspostGzipResponseWriter{
				Writer: w, ResponseWriter: rw, minLength: config.MinLength, buffer: buf,
			}
			defer func() {
				if !grw.wroteBody {
					if res.Header().Get(echo.HeaderContentEncoding) == "gzip" {
						res.Header().Del(echo.HeaderContentEncoding)
					}
					if grw.wroteHeader {
						rw.WriteHeader(grw.code)
					}
					res.Writer = rw
					w.Reset(io.Discard)
				} else if !grw.minLengthExceeded {
					res.Writer = rw
					if grw.wroteHeader {
						grw.ResponseWriter.WriteHeader(grw.code)
					}
					grw.buffer.WriteTo(rw)
					w.Reset(io.Discard)
				}
				w.Close()
				bpool.Put(buf)
				pool.Put(w)
			}()
			res.Writer = grw
			return next(c)
		}
	}
}

type klauspostGzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
	wroteHeader       bool
	wroteBody         bool
	minLength         int
	minLengthExceeded bool
	buffer            *bytes.Buffer
	code              int
}

func (w *klauspostGzipResponseWriter) WriteHeader(code int) {
	w.Header().Del(echo.HeaderContentLength)
	w.wroteHeader = true
	w.code = code
}

func (w *klauspostGzipResponseWriter) Write(b []byte) (int, error) {
	if w.Header().Get(echo.HeaderContentType) == "" {
		w.Header().Set(echo.HeaderContentType, http.DetectContentType(b))
	}
	w.wroteBody = true

	if !w.minLengthExceeded {
		n, err := w.buffer.Write(b)
		if w.buffer.Len() >= w.minLength {
			w.minLengthExceeded = true
			w.Header().Set(echo.HeaderContentEncoding, "gzip")
			if w.wroteHeader {
				w.ResponseWriter.WriteHeader(w.code)
			}
			return w.Writer.Write(w.buffer.Bytes())
		}
		return n, err
	}
	return w.Writer.Write(b)
}

func (w *klauspostGzipResponseWriter) Flush() {
	if !w.minLengthExceeded {
		w.minLengthExceeded = true
		w.Header().Set(echo.HeaderContentEncoding, "gzip")
		if w.wroteHeader {
			w.ResponseWriter.WriteHeader(w.code)
		}
		w.Writer.Write(w.buffer.Bytes())
	}
	if zw, ok := w.Writer.(*gzip.Writer); ok {
		zw.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *klauspostGzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

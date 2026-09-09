package http

import (
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPConstructorOptionalDefaults(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	for _, test := range []struct {
		name string
		opts []Option
	}{
		{name: "omitted options"},
		{name: "nil option", opts: []Option{nil}},
		{name: "nil logger", opts: []Option{WithLogger(nil)}},
		{name: "logger only", opts: []Option{WithLogger(logger)}},
		{name: "nil origins", opts: []Option{WithCORSAllowOrigins(nil)}},
		{name: "empty origins", opts: []Option{WithCORSAllowOrigins([]string{})}},
		{name: "nil logger after supplied logger", opts: []Option{WithLogger(logger), WithLogger(nil)}},
		{name: "combined nil options", opts: []Option{nil, WithLogger(nil), WithCORSAllowOrigins(nil)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newHTTPOptionsTestServer(t, test.opts...)
			server.root.POST("/constructor", func(c *echo.Context) error {
				body, err := io.ReadAll(c.Request().Body)
				if err != nil {
					return err
				}
				return c.String(nethttp.StatusOK, string(body))
			})
			request := httptest.NewRequestWithContext(
				t.Context(), nethttp.MethodPost, "/constructor", strings.NewReader("{}"),
			)
			request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			request.Header.Set(echo.HeaderOrigin, "https://unconfigured.example")
			response := httptest.NewRecorder()
			server.e.ServeHTTP(response, request)

			assert.Equal(t, nethttp.StatusOK, response.Code)
			assert.Equal(t, "{}", response.Body.String(), "a normal body must reach the handler")
			assert.Empty(t, response.Header().Get(echo.HeaderAccessControlAllowOrigin))
			assert.Empty(t, response.Header().Get(echo.HeaderAccessControlAllowCredentials))
		})
	}
}

func TestHTTPConstructorBodyLimits(t *testing.T) {
	const oneMiB = 1 << 20
	for _, test := range []struct {
		name       string
		opts       []Option
		size       int
		wantStatus int
	}{
		{name: "omitted accepts small body", size: 2, wantStatus: nethttp.StatusNoContent},
		{name: "omitted accepts limit", size: oneMiB, wantStatus: nethttp.StatusNoContent},
		{name: "omitted rejects over limit", size: oneMiB + 1, wantStatus: nethttp.StatusRequestEntityTooLarge},
		{name: "explicit accepts limit", opts: []Option{WithBodyLimit(2)}, size: 2, wantStatus: nethttp.StatusNoContent},
		{
			name: "explicit rejects over limit", opts: []Option{WithBodyLimit(2)},
			size: 3, wantStatus: nethttp.StatusRequestEntityTooLarge,
		},
		{name: "zero accepts empty body", opts: []Option{WithBodyLimit(0)}, wantStatus: nethttp.StatusNoContent},
		{
			name: "zero rejects nonempty body", opts: []Option{WithBodyLimit(0)},
			size: 1, wantStatus: nethttp.StatusRequestEntityTooLarge,
		},
		{
			name: "larger explicit limit", opts: []Option{WithBodyLimit(2 * oneMiB)},
			size: oneMiB + 1, wantStatus: nethttp.StatusNoContent,
		},
	} {
		for _, knownLength := range []bool{true, false} {
			name := test.name + "/known length"
			if !knownLength {
				name = test.name + "/unknown length"
			}
			t.Run(name, func(t *testing.T) {
				opts := []Option{
					WithLogger(slog.New(slog.DiscardHandler)),
					WithCORSAllowOrigins([]string{"https://allowed.example"}),
				}
				opts = append(opts, test.opts...)
				server := newHTTPOptionsTestServer(t, opts...)
				completed := false
				server.root.POST("/body-limit", func(c *echo.Context) error {
					if _, err := io.Copy(io.Discard, c.Request().Body); err != nil {
						return err
					}
					completed = true
					return c.NoContent(nethttp.StatusNoContent)
				})
				request := httptest.NewRequestWithContext(
					t.Context(), nethttp.MethodPost, "/body-limit", strings.NewReader(strings.Repeat("x", test.size)),
				)
				if !knownLength {
					request.ContentLength = -1
				}
				response := httptest.NewRecorder()
				server.e.ServeHTTP(response, request)

				assert.Equal(t, test.wantStatus, response.Code)
				assert.Equal(t, test.wantStatus == nethttp.StatusNoContent, completed,
					"oversized bodies must not complete processing")
			})
		}
	}
}

func TestHTTPConstructorCORSOrigins(t *testing.T) {
	const allowedOrigin = "https://allowed.example"
	const secondOrigin = "https://second.example"
	for _, test := range []struct {
		name      string
		origins   []string
		anyOrigin bool
	}{
		{name: "nil origins"},
		{name: "empty origins", origins: []string{}},
		{name: "explicit origin", origins: []string{allowedOrigin}},
		{name: "multiple explicit origins", origins: []string{allowedOrigin, secondOrigin}},
		{name: "reversed explicit origins", origins: []string{secondOrigin, allowedOrigin}},
		{name: "wildcard", origins: []string{"*"}, anyOrigin: true},
		{name: "wildcard first", origins: []string{"*", allowedOrigin, secondOrigin}, anyOrigin: true},
		{name: "wildcard middle", origins: []string{allowedOrigin, "*", secondOrigin}, anyOrigin: true},
		{name: "wildcard last", origins: []string{allowedOrigin, secondOrigin, "*"}, anyOrigin: true},
		{name: "wildcard before redundant invalid entry", origins: []string{"*", "invalid"}, anyOrigin: true},
		{name: "wildcard after redundant invalid entry", origins: []string{"invalid", "*"}, anyOrigin: true},
	} {
		for _, origin := range []string{allowedOrigin, "https://unlisted.example"} {
			for _, method := range []string{nethttp.MethodGet, nethttp.MethodOptions} {
				t.Run(test.name+"/"+method+"/"+origin, func(t *testing.T) {
					server := newHTTPOptionsTestServer(t,
						WithLogger(slog.New(slog.DiscardHandler)),
						WithCORSAllowOrigins(test.origins),
					)
					handled := false
					server.root.Match([]string{nethttp.MethodGet, nethttp.MethodOptions}, "/cors",
						func(c *echo.Context) error {
							handled = true
							return c.NoContent(nethttp.StatusNoContent)
						},
					)
					request := httptest.NewRequestWithContext(t.Context(), method, "/cors", nil)
					request.Header.Set(echo.HeaderOrigin, origin)
					if method == nethttp.MethodOptions {
						request.Header.Set(echo.HeaderAccessControlRequestMethod, nethttp.MethodPost)
						request.Header.Set(
							echo.HeaderAccessControlRequestHeaders,
							"authorization,content-type,x-api-key",
						)
					}
					response := httptest.NewRecorder()
					server.e.ServeHTTP(response, request)

					wantOrigin, wantCredentials := "", ""
					if test.anyOrigin {
						wantOrigin = "*"
					} else if len(test.origins) > 0 && origin == allowedOrigin {
						wantOrigin, wantCredentials = origin, "true"
					}
					assert.Equal(t, nethttp.StatusNoContent, response.Code)
					assert.Equal(t, wantOrigin, response.Header().Get(echo.HeaderAccessControlAllowOrigin))
					assert.Equal(t, wantCredentials, response.Header().Get(echo.HeaderAccessControlAllowCredentials))
					assert.Equal(t, method == nethttp.MethodGet || len(test.origins) == 0, handled,
						"configured CORS handles preflight; ordinary requests still reach their handler")
					if wantOrigin != "" && method == nethttp.MethodOptions {
						assert.ElementsMatch(t, []string{"Authorization", "Content-Type", "X-API-Key"},
							strings.Split(response.Header().Get(echo.HeaderAccessControlAllowHeaders), ","))
						assert.Contains(
							t,
							response.Header().Get(echo.HeaderAccessControlAllowMethods),
							nethttp.MethodPost,
						)
						assert.Equal(t, "3600", response.Header().Get(echo.HeaderAccessControlMaxAge))
					}
				})
			}
		}
	}
}

func TestHTTPConstructorOptionalFiles(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	require.NoError(t, os.WriteFile("private-marker.txt", []byte("private fixture"), 0o600))
	publicRoot := filepath.Join(directory, "public")
	require.NoError(t, os.Mkdir(publicRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(publicRoot, "asset.txt"), []byte("public fixture"), 0o600))

	for _, test := range []struct {
		name       string
		opts       []Option
		wantPublic bool
	}{
		{name: "omitted roots"},
		{name: "empty roots", opts: []Option{WithStaticRoot(""), WithSwaggerRoot("")}},
		{name: "explicit static root", opts: []Option{WithStaticRoot(publicRoot)}, wantPublic: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := []Option{
				WithLogger(slog.New(slog.DiscardHandler)),
				WithCORSAllowOrigins([]string{"https://allowed.example"}),
			}
			opts = append(opts, test.opts...)
			server := newHTTPOptionsTestServer(t, opts...)
			server.v1.GET("/ping", func(c *echo.Context) error { return c.NoContent(nethttp.StatusNoContent) })
			for _, path := range []string{"/static/private-marker.txt", "/api/v1/", "/static/asset.txt", "/api/v1/ping"} {
				response := httptest.NewRecorder()
				server.e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, path, nil))
				wantStatus := nethttp.StatusNotFound
				if path == "/static/asset.txt" && test.wantPublic {
					wantStatus = nethttp.StatusOK
					assert.Equal(t, "public fixture", response.Body.String())
				} else if path == "/api/v1/ping" {
					wantStatus = nethttp.StatusNoContent
				}
				assert.Equal(t, wantStatus, response.Code, path)
				assert.NotContains(t, response.Body.String(), "private fixture", path)
			}
		})
	}
}

func newHTTPOptionsTestServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	var server *Server
	require.NotPanics(t, func() { server = New(opts...) })
	require.NotNil(t, server)
	t.Cleanup(server.shutdownCancel)
	return server
}

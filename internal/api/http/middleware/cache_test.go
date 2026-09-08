package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/api/http/middleware/mocks"
)

func TestCacheMiddlewareBypassesCacheWhenDisabled(t *testing.T) {
	cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
	e := echo.New()
	e.Use(CacheMiddleware(cache, 0))
	const body = `{"accepted":true}`
	calls := 0
	e.GET("/items", func(c *echo.Context) error {
		calls++
		assert.Same(t, t.Context(), c.Request().Context())
		c.Response().Header().Set("X-Origin", "handler")
		return c.JSONBlob(http.StatusAccepted, []byte(body))
	})
	response := httptest.NewRecorder()

	e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items", nil))

	assert.Equal(t, 1, calls)
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, body, response.Body.String())
	assert.Equal(t, "handler", response.Header().Get("X-Origin"))
	assert.Empty(t, response.Header().Values("X-Cache"))
}

func TestCacheMiddlewareReturnsCacheHitsWithoutCallingHandler(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "JSON body", body: "{\n  \"items\": [42]\n}\n"},
		{name: "empty cached value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
			cache.EXPECT().Get(t.Context(), "GET+/items?page=2").Return(test.body, true, nil)
			e := echo.New()
			e.Use(CacheMiddleware(cache, time.Minute))
			e.GET("/items", func(*echo.Context) error {
				t.Error("a cache hit must skip the handler")
				return nil
			})
			response := httptest.NewRecorder()

			e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items?page=2", nil))

			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, test.body, response.Body.String())
			assert.Equal(t, echo.MIMEApplicationJSON, response.Header().Get(echo.HeaderContentType))
			assert.Equal(t, []string{"HIT"}, response.Header().Values("X-Cache"))
		})
	}
}

func TestCacheMiddlewareStoresAndReusesSuccessfulResponses(t *testing.T) {
	const body = "{\"items\":[{\"id\":42}]}\n"
	for _, test := range []struct {
		name   string
		method string
		target string
		key    string
		ttl    time.Duration
		body   string
	}{
		{
			name: "no query", method: http.MethodGet, target: "/items", key: "GET+/items?",
			ttl: time.Minute, body: body,
		},
		{
			name: "different method", method: http.MethodPost, target: "/items", key: "POST+/items?",
			ttl: 30 * time.Second, body: body,
		},
		{
			name: "different path", method: http.MethodGet, target: "/items/42", key: "GET+/items/42?",
			ttl: 2 * time.Minute, body: body,
		},
		{
			name: "raw query", method: http.MethodGet, target: "/items?page=2&tag=a%2Fb&tag=c",
			key: "GET+/items?page=2&tag=a%2Fb&tag=c", ttl: 1500 * time.Millisecond, body: body,
		},
		{
			name: "empty successful response", method: http.MethodGet, target: "/items", key: "GET+/items?",
			ttl: time.Minute,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
			gomock.InOrder(
				cache.EXPECT().Get(t.Context(), test.key).Return("", false, nil),
				cache.EXPECT().Set(t.Context(), test.key, test.body, test.ttl).Return(nil),
				cache.EXPECT().Get(t.Context(), test.key).Return(test.body, true, nil),
			)
			e := echo.New()
			e.Use(CacheMiddleware(cache, test.ttl))
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.target, nil)
			calls := 0
			e.Add(test.method, request.URL.Path, func(c *echo.Context) error {
				calls++
				assert.Same(t, t.Context(), c.Request().Context())
				return c.JSONBlob(http.StatusOK, []byte(test.body))
			})

			for _, cacheStatus := range []string{"MISS", "HIT"} {
				response := httptest.NewRecorder()
				e.ServeHTTP(response, request.Clone(t.Context()))
				assert.Equal(t, http.StatusOK, response.Code)
				assert.Equal(t, test.body, response.Body.String())
				assert.Equal(t, echo.MIMEApplicationJSON, response.Header().Get(echo.HeaderContentType))
				assert.Equal(t, []string{cacheStatus}, response.Header().Values("X-Cache"))
			}
			assert.Equal(t, 1, calls)
		})
	}
}

func TestCacheMiddlewareDoesNotStoreNonOKResponses(t *testing.T) {
	for _, status := range []int{
		http.StatusCreated, http.StatusNoContent, http.StatusFound, http.StatusBadRequest, http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
			cache.EXPECT().Get(t.Context(), "GET+/items?").Return("", false, nil)
			e := echo.New()
			e.Use(CacheMiddleware(cache, time.Minute))
			e.GET("/items", func(c *echo.Context) error { return c.NoContent(status) })
			response := httptest.NewRecorder()

			e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items", nil))

			assert.Equal(t, status, response.Code)
			assert.Empty(t, response.Body.String())
			assert.Equal(t, []string{"MISS"}, response.Header().Values("X-Cache"))
		})
	}
}

func TestCacheMiddlewarePropagatesHandlerErrorsWithoutCaching(t *testing.T) {
	for _, test := range []struct {
		name        string
		ttl         time.Duration
		cacheError  error
		writeBefore bool
		wantStatus  int
		wantCache   string
	}{
		{name: "disabled", wantStatus: http.StatusInternalServerError},
		{name: "cache miss", ttl: time.Minute, wantStatus: http.StatusInternalServerError, wantCache: "MISS"},
		{
			name: "cache read failure", ttl: time.Minute, cacheError: errors.New("cache unavailable"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "response written before error", ttl: time.Minute, writeBefore: true,
			wantStatus: http.StatusOK, wantCache: "MISS",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
			if test.ttl != 0 {
				cache.EXPECT().Get(t.Context(), "GET+/items?").Return("", false, test.cacheError)
			}
			e := echo.New()
			e.Use(CacheMiddleware(cache, test.ttl))
			wantErr := errors.New("handler failed")
			var receivedErr error
			errorCalls := 0
			defaultErrorHandler := e.HTTPErrorHandler
			e.HTTPErrorHandler = func(c *echo.Context, err error) {
				errorCalls++
				receivedErr = err
				defaultErrorHandler(c, err)
			}
			const body = `{"partial":true}`
			e.GET("/items", func(c *echo.Context) error {
				if test.writeBefore {
					if err := c.JSONBlob(http.StatusOK, []byte(body)); err != nil {
						return err
					}
				}
				return wantErr
			})
			response := httptest.NewRecorder()

			e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items", nil))

			require.ErrorIs(t, receivedErr, wantErr)
			assert.Equal(t, 1, errorCalls)
			assert.Equal(t, test.wantStatus, response.Code)
			assert.Equal(t, test.wantCache, response.Header().Get("X-Cache"))
			if test.writeBefore {
				assert.Equal(t, body, response.Body.String())
			} else {
				assert.JSONEq(t, `{"message":"Internal Server Error"}`, response.Body.String())
			}
		})
	}
}

func TestCacheMiddlewareFallsBackAfterReadFailure(t *testing.T) {
	for _, found := range []bool{false, true} {
		name := "no cached value"
		if found {
			name = "cached value returned with error"
		}
		t.Run(name, func(t *testing.T) {
			cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
			cache.EXPECT().
				Get(t.Context(), "GET+/items?").
				Return(`{"stale":true}`, found, errors.New("cache unavailable"))
			e := echo.New()
			e.Use(CacheMiddleware(cache, time.Minute))
			const body = `{"fresh":true}`
			calls := 0
			e.GET("/items", func(c *echo.Context) error {
				calls++
				c.Response().Header().Set("X-Origin", "handler")
				return c.JSONBlob(http.StatusOK, []byte(body))
			})
			response := httptest.NewRecorder()

			e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items", nil))

			assert.Equal(t, 1, calls)
			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, body, response.Body.String())
			assert.Equal(t, "handler", response.Header().Get("X-Origin"))
			assert.Empty(t, response.Header().Values("X-Cache"))
		})
	}
}

func TestCacheMiddlewarePreservesResponseAfterWriteFailure(t *testing.T) {
	cache := mocks.NewMockcacheAccessor(gomock.NewController(t))
	const body = `{"fresh":true}`
	gomock.InOrder(
		cache.EXPECT().Get(t.Context(), "GET+/items?").Return("", false, nil),
		cache.EXPECT().Set(t.Context(), "GET+/items?", body, time.Minute).Return(errors.New("cache unavailable")),
	)
	e := echo.New()
	e.Use(CacheMiddleware(cache, time.Minute))
	e.HTTPErrorHandler = func(_ *echo.Context, err error) {
		t.Errorf("cache write failure reached the HTTP error handler: %v", err)
	}
	e.GET("/items", func(c *echo.Context) error {
		c.Response().Header().Set("X-Origin", "handler")
		return c.JSONBlob(http.StatusOK, []byte(body))
	})
	response := httptest.NewRecorder()

	e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/items", nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, body, response.Body.String())
	assert.Equal(t, echo.MIMEApplicationJSON, response.Header().Get(echo.HeaderContentType))
	assert.Equal(t, "handler", response.Header().Get("X-Origin"))
	assert.Equal(t, []string{"MISS"}, response.Header().Values("X-Cache"))
}

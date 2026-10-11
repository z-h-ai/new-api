package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoloClientRequestCorrelation(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	router := gin.New()
	router.Use(RequestId())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		assert.Equal(t, nonce, c.GetString("client_request_id"))
		assert.Empty(t, c.GetHeader("X-Polo-Request-Id"))
		assert.Equal(t, "Bearer original-test-key", c.GetHeader("Authorization"))
		assert.NotEqual(t, nonce, c.GetString(common.RequestIdKey))
		c.Status(http.StatusOK)
	})
	var originalIDs []string
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer original-test-key")
		req.Header.Set("X-Polo-Request-Id", nonce)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		originalIDs = append(originalIDs, w.Header().Get(common.RequestIdKey))
	}
	assert.NotEqual(t, originalIDs[0], originalIDs[1], "a repeated correlation nonce does not replace or authenticate the gateway request ID")
}

func TestPoloClientRequestCorrelationRejectsMalformedHeader(t *testing.T) {
	for _, values := range [][]string{{""}, {"short"}, {"0123456789ABCDEF0123456789ABCDEF"}, {"0123456789abcdef0123456789abcdeg"}, {"0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"}} {
		t.Run(values[0], func(t *testing.T) {
			router := gin.New()
			router.Use(RequestId())
			router.POST("/v1/chat/completions", func(c *gin.Context) { t.Fatal("invalid correlation must never reach the relay") })
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for _, value := range values {
				req.Header.Add("X-Polo-Request-Id", value)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestPoloClientRequestCorrelationIsOptional(t *testing.T) {
	router := gin.New()
	router.Use(RequestId())
	router.GET("/api/status", func(c *gin.Context) { assert.Empty(t, c.GetString("client_request_id")); c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

package middleware

import (
	"context"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		id := common.NewRequestId()
		c.Set(common.RequestIdKey, id)
		ctx := context.WithValue(c.Request.Context(), common.RequestIdKey, id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(common.RequestIdKey, id)
		values := c.Request.Header.Values(common.ClientRequestIdHeader)
		// Remove private metadata before any relay consumer copies headers.
		c.Request.Header.Del(common.ClientRequestIdHeader)
		if len(values) != 0 {
			if len(values) != 1 || !common.IsValidClientRequestId(values[0]) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_client_request_id", "message": "invalid request correlation metadata"}})
				return
			}
			c.Set(common.ClientRequestIdKey, values[0])
		}
		c.Next()
	}
}

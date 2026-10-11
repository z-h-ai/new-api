package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoloCorrelationConsumeAndErrorLog(t *testing.T) {
	user, _ := setupAccessTokenAudit(t)
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport })
	nonce := "0123456789abcdef0123456789abcdef"
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("client_request_id", nonce)
	ctx.Set(common.RequestIdKey, "gateway-independent-id")
	ctx.Set("username", user.Username)
	model.RecordConsumeLog(ctx, user.Id, model.RecordConsumeLogParams{ModelName: "original-model", TokenId: 19, Quota: 37, PromptTokens: 7, CompletionTokens: 3, Other: model.NewLogOther()})
	model.RecordErrorLog(ctx, user.Id, 0, "original-model", "original-token", "controlled failure", 19, 1, false, "default", model.NewLogOther())
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Order("id").Find(&logs).Error)
	require.Len(t, logs, 2)
	for _, log := range logs {
		encoded, err := common.Marshal(log)
		require.NoError(t, err)
		var data map[string]any
		require.NoError(t, common.Unmarshal(encoded, &data))
		assert.Equal(t, nonce, data["client_request_id"])
		assert.Equal(t, "gateway-independent-id", log.RequestId)
		assert.Equal(t, user.Id, log.UserId)
		assert.Equal(t, 19, log.TokenId)
	}
	assert.Equal(t, 37, logs[0].Quota)
	assert.Equal(t, 7, logs[0].PromptTokens)
	assert.Equal(t, 3, logs[0].CompletionTokens)
}

func TestPoloCorrelationStatusCapability(t *testing.T) {
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	GetStatus(ctx)
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, map[string]any{"version": float64(1), "header": "X-Polo-Request-Id", "log_field": "client_request_id", "query_parameter": "client_request_id"}, payload.Data["polo_request_correlation"])
}

func TestPoloCorrelationAdminQueryRequiresExistingAuthentication(t *testing.T) {
	router := gin.New()
	router.GET("/api/log/", middleware.AdminAuth(), GetAllLogs)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/log/?client_request_id=0123456789abcdef0123456789abcdef", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestPoloCorrelationAdminExactQueryAndPrivacy(t *testing.T) {
	user, _ := setupAccessTokenAudit(t)
	_, jwt := createAccessTokenTestSession(t, user.Id, "polo-query-session")
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport })
	const nonce = "0123456789abcdef0123456789abcdef"
	for i, value := range []string{nonce, "abcdef0123456789abcdef0123456789", nonce} {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("client_request_id", value)
		ctx.Set(common.RequestIdKey, []string{"gateway-first", "gateway-other", "gateway-last"}[i])
		ctx.Set("username", user.Username)
		model.RecordConsumeLog(ctx, user.Id, model.RecordConsumeLogParams{ModelName: "original-model", TokenName: "original-token", TokenId: 19, Quota: 37, Other: model.NewLogOther()})
	}
	router := gin.New()
	router.Use(middleware.RequestId())
	router.GET("/api/log/", middleware.AdminAuth(), GetAllLogs)
	for _, page := range []string{"1", "2"} {
		w := auditRequest(router, http.MethodGet, "/api/log/?type=2&client_request_id="+nonce+"&p="+page+"&page_size=1", jwt)
		require.Equal(t, http.StatusOK, w.Code)
		var payload struct {
			Success bool `json:"success"`
			Data    struct {
				Total int              `json:"total"`
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		assert.Equal(t, 2, payload.Data.Total, "a repeated nonce remains ambiguous, never deduplicated")
		require.Len(t, payload.Data.Items, 1)
		assert.Equal(t, nonce, payload.Data.Items[0]["client_request_id"])
		assert.Equal(t, "original-model", payload.Data.Items[0]["model_name"])
		assert.Equal(t, float64(19), payload.Data.Items[0]["token_id"])
		expectedID := "gateway-last"
		if page == "2" {
			expectedID = "gateway-first"
		}
		assert.Equal(t, expectedID, payload.Data.Items[0]["request_id"])
	}
	for _, query := range []string{"bad", nonce + "&client_request_id=" + nonce} {
		w := auditRequest(router, http.MethodGet, "/api/log/?client_request_id="+query, jwt)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
	w := auditRequest(router, http.MethodGet, "/api/log/?type=2&client_request_id="+nonce+"&request_id=gateway-other", jwt)
	assert.Contains(t, w.Body.String(), `"total":0`, "original gateway filters remain conjunctive")
	w = auditRequest(router, http.MethodGet, "/api/log/?type=2", jwt)
	assert.Contains(t, w.Body.String(), `"total":3`, "legacy queries retain their original scope")
	member := &model.User{Username: "polo-member", Password: "placeholder", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "polo-member"}
	require.NoError(t, model.DB.Create(member).Error)
	_, memberJWT := createAccessTokenTestSession(t, member.Id, "polo-member-query-session")
	w = auditRequest(router, http.MethodGet, "/api/log/?client_request_id="+nonce, memberJWT)
	assert.Equal(t, http.StatusForbidden, w.Code, "a known nonce never grants admin authority")
	assert.True(t, model.LOG_DB.Migrator().HasIndex(&model.Log{}, "idx_logs_client_request_id"))
	logs, err := model.GetLogByTokenId(19)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	for _, log := range logs {
		encoded, err := common.Marshal(log)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "client_request_id", "private Host correlation is not returned to a token/user reader")
	}
}

func TestPoloClientRequestCorrelation(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	router := gin.New()
	router.Use(middleware.RequestId())
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
			router.Use(middleware.RequestId())
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
	router.Use(middleware.RequestId())
	router.GET("/api/status", func(c *gin.Context) { assert.Empty(t, c.GetString("client_request_id")); c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

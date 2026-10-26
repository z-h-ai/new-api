package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagedPermitGuardsActualWireBeforeConsumption(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(previous)) })
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.ManagedReservation{}))
	user := model.User{Username: "permit-user", Group: "default", Quota: 10000, AffCode: "permit-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "permit-test-key", Status: common.TokenStatusEnabled, RemainQuota: 10000}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, model.UpdateOption("billing_setting.billing_expr", `{"permit-model":"p*0.8+c*2.8","qwen3-max":"p*0.8+c*2.8"}`))
	require.NoError(t, model.UpdateOption("billing_setting.billing_mode", `{"permit-model":"tiered_expr","qwen3-max":"tiered_expr"}`))
	raw := `{"model":"permit-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`
	issue := func(i int, body string, input, output int) string {
		nonce := fmt.Sprintf("%032x", i)
		hash := sha256.Sum256([]byte(body))
		quote, err := managedTextReservation("permit-model", token.Id, input, output)
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.ManagedReservation{Nonce: nonce, TokenID: token.Id, Model: "permit-model", BodySHA256: hex.EncodeToString(hash[:]), InputTokens: input, OutputTokens: output, NativeQuota: quote["native_quota"].(int), ExpiresAt: time.Now().Add(time.Minute).Unix()}).Error)
		return nonce
	}
	check := func(nonce, body string, tokenID int) error {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyTokenId, tokenID)
		c.Set(common.ClientRequestIdKey, nonce)
		defer common.CleanupBodyStorage(c)
		return enforceManagedReservation(c)
	}
	nonce := issue(1, raw, 1000, 8)
	require.Error(t, check(nonce, strings.Replace(raw, "hello", "larger prompt", 1), token.Id))
	require.Error(t, check("", raw, token.Id))
	require.NoError(t, check(nonce, raw, token.Id))
	require.Error(t, check(nonce, raw, token.Id))
	require.Error(t, check(issue(2, raw, 1, 8), raw, token.Id))
	require.Error(t, check(issue(3, raw, 1000, 1), raw, token.Id))
	tool := strings.Replace(raw, `"max_tokens":8`, `"max_tokens":8,"web_search_options":{}`, 1)
	require.Error(t, check(issue(4, tool, 1000, 8), tool, token.Id))
	schema := strings.Replace(raw, `"max_tokens":8`, `"max_tokens":8,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"file_id":{"type":"string"},"audio":{"type":"string"},"tools":{"type":"string"}}}}}]`, 1)
	require.NoError(t, check(issue(6, schema, 2000, 8), schema, token.Id))
	expired := issue(5, raw, 1000, 8)
	require.NoError(t, db.Model(&model.ManagedReservation{}).Where("nonce = ?", expired).Update("expires_at", 0).Error)
	require.Error(t, check(expired, raw, token.Id))
	quote, err := managedTextReservation("qwen3-max@thinking:off", token.Id, 1000, 8)
	require.NoError(t, err)
	require.Equal(t, "qwen3-max", quote["billing_model"])
	_, err = managedTextReservation("gpt-4o-search-preview", token.Id, 1000, 8)
	require.ErrorContains(t, err, "tool surcharge")
	var after model.User
	require.NoError(t, db.First(&after, user.Id).Error)
	require.Equal(t, 10000, after.Quota)
}

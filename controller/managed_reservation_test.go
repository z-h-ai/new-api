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
	"os"
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

func TestManagedPermitClosureCompetesWithOriginalConsumption(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip(dialect.env + " is not configured")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ManagedReservation{}))
			const bodyHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			issue := func(number int) string {
				nonce := fmt.Sprintf("%032x", number)
				require.NoError(t, db.Create(&model.ManagedReservation{Nonce: nonce, TokenID: 7, Model: "permit-model", BodySHA256: bodyHash, ExpiresAt: time.Now().Add(time.Minute).Unix()}).Error)
				return nonce
			}
			closePermit := func(nonce string, overrides map[string]any) (int, string) {
				payload := map[string]any{"nonce": nonce, "token_id": 7, "model": "permit-model", "body_sha256": bodyHash}
				for key, value := range overrides {
					payload[key] = value
				}
				raw, err := common.Marshal(payload)
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest("POST", "/api/option/model_pricing/reservation/close", strings.NewReader(string(raw)))
				CloseManagedTextReservation(context)
				var response struct {
					Data struct { State string `json:"state"` } `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				return recorder.Code, response.Data.State
			}
			closed := issue(101)
			oldNow := time.Now().Unix()
			status, state := closePermit(closed, nil)
			require.Equal(t, 200, status)
			require.Equal(t, "closed_unused", state)
			// A consume UPDATE sent with an earlier timestamp cannot win later.
			late := db.Model(&model.ManagedReservation{}).Where("nonce = ? AND consumed = ? AND expires_at > ?", closed, false, oldNow).Update("consumed", true)
			require.NoError(t, late.Error)
			require.Zero(t, late.RowsAffected)
			status, state = closePermit(closed, nil)
			require.Equal(t, 200, status)
			require.Equal(t, "closed_unused", state)
			consumed := issue(102)
			require.NoError(t, db.Model(&model.ManagedReservation{}).Where("nonce = ? AND consumed = ?", consumed, false).Update("consumed", true).Error)
			status, state = closePermit(consumed, nil)
			require.Equal(t, 200, status)
			require.Equal(t, "consumed", state)
			for _, override := range []map[string]any{{"token_id": 8}, {"model": "another-model"}, {"body_sha256": strings.Repeat("b", 64)}, {"unknown": true}} {
				status, state = closePermit(issue(103+len(override)), override)
				require.NotEqual(t, 200, status)
				require.Empty(t, state)
				// Reuse a fresh row in the next table case.
				require.NoError(t, db.Where("nonce = ?", fmt.Sprintf("%032x", 104)).Delete(&model.ManagedReservation{}).Error)
			}
			status, state = closePermit(fmt.Sprintf("%032x", 999), nil)
			require.NotEqual(t, 200, status)
			require.Empty(t, state)
		})
	}
}

package controller

import (
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// QuoteManagedTextReservation creates a root-issued single-use permit without
// a model call, wallet write, token creation, or replacement pricing table. Polo binds this quote before
// sending the bounded request; actual settlement still uses supplier evidence.
func QuoteManagedTextReservation(c *gin.Context) {
	var request struct {
		Nonce    string `json:"nonce"`
		BodyHash string `json:"body_sha256"`
		Model    string `json:"model"`
		TokenID  int    `json:"token_id"`
		Input    uint   `json:"input_tokens"`
		Output   uint   `json:"max_output_tokens"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || !common.IsValidClientRequestId(request.Nonce) || !validReservationHash(request.BodyHash) || request.Model == "" || request.TokenID <= 0 || request.Input == 0 || request.Output == 0 || helper.ExceedsMaxTokensLimit(&request.Input, &request.Output) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "bounded model/token/input/output required"})
		return
	}
	quote, err := managedTextReservation(request.Model, request.TokenID, int(request.Input), int(request.Output))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		return
	}
	permit := model.ManagedReservation{Nonce: request.Nonce, TokenID: request.TokenID, Model: request.Model, BodySHA256: request.BodyHash, InputTokens: int(request.Input), OutputTokens: int(request.Output), NativeQuota: quote["native_quota"].(int), ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	if err := model.DB.Create(&permit).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "reservation identity already used"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	common.ApiSuccess(c, quote)
}

func managedTextReservation(name string, tokenID, input, output int) (gin.H, error) {
	token, err := model.GetTokenById(tokenID)
	if err != nil {
		return nil, err
	}
	if token.Status != common.TokenStatusEnabled {
		return nil, fmt.Errorf("provider token unavailable")
	}
	userGroup, err := model.GetUserGroup(token.UserId, true)
	if err != nil {
		return nil, err
	}
	group := token.Group
	if group == "" {
		group = userGroup
	}
	if group == "auto" {
		return nil, fmt.Errorf("auto-group reservation requires a bounded group set")
	}
	ratio := ratio_setting.GetGroupRatio(group)
	if custom, ok := ratio_setting.GetGroupGroupRatio(userGroup, group); ok {
		ratio = custom
	}
	if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return nil, fmt.Errorf("invalid group ratio")
	}
	billingModel := helper.ResolveBillingModelName(name)
	if strings.HasSuffix(billingModel, "search-preview") {
		return nil, fmt.Errorf("tool surcharge cannot be bounded for this model")
	}
	snapshot, err := model.GetModelPricingSnapshot([]string{billingModel})
	if err != nil {
		return nil, err
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].ModelName != billingModel {
		return nil, fmt.Errorf("original model price unavailable")
	}
	entry := snapshot.Entries[0]
	expression, _ := entry.Effective["billing_setting.billing_expr"].(string)
	if entry.Effective["billing_setting.billing_mode"] != billing_setting.BillingModeTieredExpr {
		converted, err := model.PreviewModelPricingConversion(billingModel, entry.Configured)
		if err != nil {
			return nil, err
		}
		if converted.Expression == "" || converted.UnsupportedReason != "" {
			return nil, fmt.Errorf("model price cannot be safely bounded")
		}
		expression = converted.Expression
	}
	bound, err := billingexpr.ReservationUpperBound(expression, input, output)
	if err != nil {
		return nil, err
	}
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return nil, fmt.Errorf("invalid native quota unit")
	}
	quota, err := common.QuotaRoundStrict(math.Ceil(bound / 1e6 * common.QuotaPerUnit * ratio))
	if err != nil {
		return nil, err
	}
	return gin.H{"model": name, "billing_model": billingModel, "token_id": token.Id, "token_owner_user_id": token.UserId, "input_tokens": input, "max_output_tokens": output, "native_quota": quota, "pricing_version": entry.Version, "expression_hash": billingexpr.ExprHashString(expression), "group": group, "group_ratio": ratio, "native_quota_per_unit": common.QuotaPerUnit}, nil
}

func validReservationHash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}

// EnableManagedToken is root-only and owner-bound. It preserves current quota
// and refuses a flag downgrade by offering no disabling operation.
func EnableManagedToken(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, fmt.Errorf("invalid original token"))
		return
	}
	token, err := model.GetTokenByIds(id, c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err = model.EnableManagedReservation(token); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"id": id, "managed_reservation_required": true})
}

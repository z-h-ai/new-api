package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"io"
	"math"
	"net/http"
	"time"
)

// Called before any upstream request. An unregistered token keeps its original
// behavior; once root binds it to managed billing, every call needs a permit.
func enforceManagedReservation(c *gin.Context) error {
	tokenID := common.GetContextKeyInt(c, constant.ContextKeyTokenId)
	managed, err := model.RequiresManagedReservation(tokenID)
	if err != nil {
		return err
	}
	if !managed {
		return nil
	}

	nonce := c.GetString(common.ClientRequestIdKey)
	if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/chat/completions" || c.Request.URL.RawQuery != "" || !common.IsValidClientRequestId(nonce) {
		return fmt.Errorf("managed request requires its original bounded permit")
	}
	var permit model.ManagedReservation
	if err := model.DB.Where("nonce = ? AND token_id = ? AND consumed = ? AND expires_at > ?", nonce, tokenID, false, time.Now().Unix()).First(&permit).Error; err != nil {
		return fmt.Errorf("managed permit unavailable")
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	reader, err := storage.NewReader()
	if err != nil {
		return err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, 32*1024*1024+1))
	if err != nil || len(raw) > 32*1024*1024 {
		return fmt.Errorf("managed body exceeds bound")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != permit.BodySHA256 {
		return fmt.Errorf("managed body differs from original permit")
	}
	var body map[string]any
	if common.Unmarshal(raw, &body) != nil {
		return fmt.Errorf("invalid managed body")
	}
	messages, _ := body["messages"].([]any)
	input := len(raw) + 256 + len(messages)*64
	output, ok := body["max_tokens"].(float64)
	completion, hasCompletion := body["max_completion_tokens"].(float64)
	if ok && hasCompletion {
		return fmt.Errorf("ambiguous managed output limit")
	}
	if hasCompletion {
		output = completion
		ok = true
	}
	countChoices := 1.0
	if n, exists := body["n"]; exists {
		var valid bool
		countChoices, valid = n.(float64)
		if !valid {
			return fmt.Errorf("invalid choice count")
		}
	}
	if body["model"] != permit.Model || input > permit.InputTokens || !ok || output < 1 || math.Trunc(output) != output || countChoices < 1 || math.Trunc(countChoices) != countChoices || output*countChoices > float64(permit.OutputTokens) || unsupportedManagedInput(body) {
		return fmt.Errorf("managed request exceeds bounded text permit")
	}
	quote, err := managedTextReservation(permit.Model, tokenID, permit.InputTokens, permit.OutputTokens)
	if err != nil {
		return err
	}
	if quote["native_quota"].(int) > permit.NativeQuota {
		return fmt.Errorf("provider price increased; obtain a new reservation")
	}
	result := model.DB.Model(&model.ManagedReservation{}).Where("nonce = ? AND consumed = ? AND expires_at > ?", nonce, false, time.Now().Unix()).Update("consumed", true)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("managed permit already consumed")
	}
	return nil
}

func unsupportedManagedInput(value any) bool {
	body, ok := value.(map[string]any)
	if !ok {
		return true
	}
	for _, key := range []string{"web_search_options", "google_search", "audio"} {
		if _, exists := body[key]; exists {
			return true
		}
	}
	if modalities, exists := body["modalities"]; exists {
		list, ok := modalities.([]any)
		if !ok || len(list) != 1 || list[0] != "text" {
			return true
		}
	}
	if tools, exists := body["tools"]; exists {
		list, ok := tools.([]any)
		if !ok {
			return true
		}
		for _, tool := range list {
			row, ok := tool.(map[string]any)
			if !ok || row["type"] != "function" {
				return true
			}
		}
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		return true
	}
	for _, message := range messages {
		row, ok := message.(map[string]any)
		if !ok {
			return true
		}
		if _, exists := row["audio"]; exists {
			return true
		}
		if parts, ok := row["content"].([]any); ok {
			for _, part := range parts {
				value, ok := part.(map[string]any)
				if !ok || value["type"] != "text" {
					return true
				}
			}
		}
	}
	return false
}

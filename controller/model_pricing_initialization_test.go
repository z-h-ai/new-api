package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This deterministic callback simulates a winning row visible during initial
// insertion. It proves stale snapshots cannot drive a write; it is not a
// cross-process database concurrency test.
func TestPoloModelPricingInitializationRereadsWinningRows(t *testing.T) {
	for _, sameModel := range []bool{true, false} {
		name := "disjoint_winner"
		if sameModel {
			name = "same_model_conflict"
		}
		t.Run(name, func(t *testing.T) {
			db := modelManagementDB(t, "sqlite", "")
			require.NoError(t, db.Exec("DELETE FROM options").Error)
			winner := "polo-other"
			if sameModel {
				winner = "polo-target"
			}
			injected := false
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("polo_initialization_winner", func(tx *gorm.DB) {
				if injected || tx.Statement.Table != "options" {
					return
				}
				injected = true
				err := tx.Exec("INSERT INTO options (key, value) VALUES (?, ?)", "ModelRatio", `{"`+winner+`":9}`).Error
				if err != nil {
					tx.AddError(err)
				}
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove("polo_initialization_winner")) })
			err := model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: "polo-target", ExpectedVersion: model.ModelPricingVersion(model.PricingValues{}), Pricing: model.PricingValues{"ModelRatio": float64(2)}}})
			require.True(t, injected)
			if sameModel {
				assert.ErrorIs(t, err, model.ErrModelPricingConflict)
				return
			}
			require.NoError(t, err)
			snapshot, err := model.GetModelPricingSnapshot([]string{winner})
			require.NoError(t, err)
			require.Len(t, snapshot.Entries, 1)
			assert.Equal(t, float64(9), snapshot.Entries[0].Configured["ModelRatio"], "initialization must preserve a disjoint winning model")
		})
	}
}

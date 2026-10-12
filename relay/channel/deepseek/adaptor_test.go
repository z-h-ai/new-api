package deepseek

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekMaxTokenSerialization(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"completion alias", `{"model":"deepseek-flash","max_completion_tokens":16}`, `{"model":"deepseek-flash","max_tokens":16}`},
		{"native limit", `{"model":"deepseek-flash","max_tokens":32}`, `{"model":"deepseek-flash","max_tokens":32}`},
		{"native larger wins", `{"model":"deepseek-flash","max_tokens":32,"max_completion_tokens":16}`, `{"model":"deepseek-flash","max_tokens":32}`},
		{"native smaller wins", `{"model":"deepseek-flash","max_tokens":16,"max_completion_tokens":32}`, `{"model":"deepseek-flash","max_tokens":16}`},
		{"native explicit zero wins", `{"model":"deepseek-flash","max_tokens":0,"max_completion_tokens":16}`, `{"model":"deepseek-flash","max_tokens":0}`},
		{"alias explicit zero", `{"model":"deepseek-flash","max_completion_tokens":0}`, `{"model":"deepseek-flash","max_tokens":0}`},
		{"both omitted", `{"model":"deepseek-flash"}`, `{"model":"deepseek-flash"}`},
		{"native null uses alias", `{"model":"deepseek-flash","max_tokens":null,"max_completion_tokens":16}`, `{"model":"deepseek-flash","max_tokens":16}`},
		{"both null remain omitted", `{"model":"deepseek-flash","max_tokens":null,"max_completion_tokens":null}`, `{"model":"deepseek-flash"}`},
		{"stream and thinking unchanged", `{"model":"deepseek-flash","max_completion_tokens":16,"stream":true,"stream_options":{"include_usage":true},"thinking":{"type":"disabled"}}`, `{"model":"deepseek-flash","max_tokens":16,"stream":true,"stream_options":{"include_usage":true},"thinking":{"type":"disabled"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request dto.GeneralOpenAIRequest
			require.NoError(t, common.UnmarshalJsonStr(tc.input, &request))
			converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, nil, &request)
			require.NoError(t, err)
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(body))
		})
	}
}

func TestDeepSeekMaxTokenNilRequest(t *testing.T) {
	_, err := (&Adaptor{}).ConvertOpenAIRequest(nil, nil, nil)
	require.EqualError(t, err, "request is nil")
}

package chat_completions

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Claude,
		ConvertOpenAIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAI,
			NonStream: ConvertClaudeResponseToOpenAINonStream,
		},
	)
}

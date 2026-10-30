package gemini

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		Gemini,
		Claude,
		ConvertGeminiRequestToClaude,
		interfaces.TranslateResponse{
			Stream:     ConvertClaudeResponseToGemini,
			NonStream:  ConvertClaudeResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}

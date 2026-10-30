package interactions

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		Interactions,
		Claude,
		ConvertInteractionsRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToInteractions,
			NonStream: ConvertClaudeResponseToInteractionsNonStream,
		},
	)
}

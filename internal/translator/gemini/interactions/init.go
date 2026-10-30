package interactions

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		Interactions,
		Interactions,
		ConvertInteractionsRequestToInteractions,
		interfaces.TranslateResponse{
			Stream:    ConvertInteractionsResponsePassthrough,
			NonStream: ConvertInteractionsResponsePassthroughNonStream,
		},
	)
	translator.Register(
		Interactions,
		Gemini,
		ConvertInteractionsRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToInteractions,
			NonStream: ConvertGeminiResponseToInteractionsNonStream,
		},
	)
	translator.Register(
		Gemini,
		Interactions,
		ConvertGeminiRequestToInteractions,
		interfaces.TranslateResponse{
			Stream:    ConvertInteractionsResponseToGemini,
			NonStream: ConvertInteractionsResponseToGeminiNonStream,
		},
	)
}

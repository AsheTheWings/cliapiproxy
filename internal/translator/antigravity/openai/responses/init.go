package responses

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
	sdktranslator "github.com/AsheTheWings/cliapiproxy/sdk/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Antigravity,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAIResponses,
			NonStream: ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatAntigravity,
		ConvertOpenAIResponsesRequestEnvelopeToAntigravity,
	)
}

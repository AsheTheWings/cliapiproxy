package responses

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Gemini,
		ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToOpenAIResponses,
			NonStream: ConvertGeminiResponseToOpenAIResponsesNonStream,
		},
	)
}

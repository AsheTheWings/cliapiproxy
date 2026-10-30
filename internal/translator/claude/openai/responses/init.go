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
		Claude,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAIResponses,
			NonStream: ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)
	// The envelope form receives the request context, which carries the
	// upstream.claude.tool-search value the executor pinned for the request.
	sdktranslator.RegisterRequestEnvelope(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		ConvertOpenAIResponsesRequestEnvelopeToClaude,
	)
}

package interactions

import (
	. "github.com/AsheTheWings/cliapiproxy/internal/constant"
	"github.com/AsheTheWings/cliapiproxy/internal/interfaces"
	"github.com/AsheTheWings/cliapiproxy/internal/translator/translator"
)

func init() {
	translator.Register(
		Interactions,
		Antigravity,
		ConvertInteractionsRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToInteractions,
			NonStream: ConvertAntigravityResponseToInteractionsNonStream,
		},
	)
}

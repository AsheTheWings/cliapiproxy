package helps

import (
	"strings"

	"github.com/AsheTheWings/cliapiproxy/internal/util"
	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	sdktranslator "github.com/AsheTheWings/cliapiproxy/sdk/translator"
)

// IsNativeCodexRequest checks the client dialect for use inside Codex executors.
func IsNativeCodexRequest(body []byte, opts cliproxyexecutor.Options) bool {
	for _, format := range []sdktranslator.Format{opts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(opts)} {
		name := strings.TrimSpace(format.String())
		if !strings.EqualFold(name, sdktranslator.FormatCodex.String()) && !strings.EqualFold(name, sdktranslator.FormatOpenAIResponse.String()) {
			return false
		}
	}
	return util.IsCodexResponsesLiteRequest(body, opts.Headers)
}

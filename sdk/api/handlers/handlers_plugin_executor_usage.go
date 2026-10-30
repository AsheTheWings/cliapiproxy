package handlers

import (
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

func parsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	return helps.ParsePluginExecutorResponseUsage(protocol, payload)
}

func observePluginExecutorStreamUsage(protocol string, payload []byte, buffer *helps.StreamUsageBuffer) {
	helps.ObservePluginExecutorStreamUsage(protocol, payload, buffer)
}

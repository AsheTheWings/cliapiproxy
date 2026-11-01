package capture

import (
	"context"

	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
)

// Stream passes result's chunks through and finishes the capture when the
// stream ends: a chunk carrying an error, or a client that went away first,
// stores nothing.
func (c *Capture) Stream(ctx context.Context, result *cliproxyexecutor.StreamResult) *cliproxyexecutor.StreamResult {
	if c == nil || result == nil || result.Chunks == nil {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	in := result.Chunks
	go func() {
		defer close(out)
		var errStream error
		for chunk := range in {
			if chunk.Err != nil && errStream == nil {
				errStream = chunk.Err
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				c.Finish(ctx, ctx.Err())
				// Drain the producer so it can finish and release its
				// connection.
				for range in {
				}
				return
			}
		}
		c.Finish(ctx, errStream)
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// ClientInput returns the bytes a client sent: the request before
// translation, else the payload.
func ClientInput(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}

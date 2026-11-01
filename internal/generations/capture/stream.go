package capture

import (
	"context"

	cliproxyexecutor "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/executor"
	"github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/usage"
)

// Stream passes result's chunks through and finishes the capture when the
// stream ends: a chunk carrying an error, or a client that went away first,
// stores nothing. A consumer that acknowledges delivery, as the Responses
// handler does once it has written response.completed, cancels the
// execution after a success; that acknowledgement decides the outcome.
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
				// Drain the producer so it can finish and release its
				// connection; it closes its channel before it waits for
				// the acknowledgement.
				for rest := range in {
					if rest.Err != nil && errStream == nil {
						errStream = rest.Err
					}
				}
				c.finishCanceled(ctx, errStream)
				return
			}
		}
		if ctx.Err() != nil {
			c.finishCanceled(ctx, errStream)
			return
		}
		c.Finish(ctx, errStream)
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// finishCanceled ends a stream whose context was canceled: it completed only
// when the consumer acknowledged delivery without an error.
func (c *Capture) finishCanceled(ctx context.Context, errStream error) {
	deliveryErr, tracked := usage.WaitStreamDelivery(ctx)
	c.end(errStream == nil && tracked && deliveryErr == nil)
}

// ClientInput returns the bytes a client sent: the request before
// translation, else the payload.
func ClientInput(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}

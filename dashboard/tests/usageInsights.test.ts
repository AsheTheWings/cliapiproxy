import { describe, expect, test } from 'bun:test';
import eventFixture from '../../internal/generations/capture/testdata/events.json';
import {
  cacheShare,
  emptyTokens,
  reasoningShare,
  viewPayload,
} from '../src/features/usage/insights';

const tokens = (values: Partial<ReturnType<typeof emptyTokens>>) => ({
  ...emptyTokens(),
  ...values,
});

describe('token shares', () => {
  test('cache share is cache-read tokens over all input tokens', () => {
    expect(
      cacheShare(tokens({ input_uncached: 50, input_cache_read: 30, input_cache_write: 20 }))
    ).toBe(0.3);
  });

  test('reasoning share is reasoning tokens over all output tokens', () => {
    expect(reasoningShare(tokens({ output_non_reasoning: 75, output_reasoning: 25 }))).toBe(0.25);
  });

  test('a share is absent, not zero, when its denominator is zero', () => {
    expect(cacheShare(tokens({ output_reasoning: 10 }))).toBeNull();
    expect(reasoningShare(tokens({ input_cache_read: 10 }))).toBeNull();
    expect(cacheShare(tokens({ input_uncached: 10 }))).toBe(0);
  });
});

describe('payload views', () => {
  test('a JSON body is pretty-printed', () => {
    expect(viewPayload('{"a":[1]}', 'application/json')).toEqual({
      kind: 'json',
      text: '{\n  "a": [\n    1\n  ]\n}',
    });
  });

  test('a server-sent event stream becomes its events', () => {
    const body = eventFixture.body;
    expect(viewPayload(body, 'text/event-stream')).toEqual({
      kind: 'events',
      events: [
        { event: 'response.created', data: '{"type":"response.created","id":1}' },
        { event: 'response.completed', data: '{"type":"response.completed","id":2}' },
      ],
    });
  });

  test('one JSON frame per line becomes a frame sequence, and other text stays text', () => {
    expect(viewPayload('{"a":1}\n{"b":2}\n', 'application/json')).toEqual({
      kind: 'events',
      events: [{ data: '{"a":1}' }, { data: '{"b":2}' }],
    });
    expect(viewPayload('<html>', 'text/html')).toEqual({ kind: 'text', text: '<html>' });
  });
});

/**
 * Calculations behind the usage charts and tables, kept free of React so
 * tests can check them directly.
 */
import type { Bucket, Tokens } from '@/services/api/store';

export const TOKEN_COLUMNS = [
  'input_uncached',
  'input_cache_read',
  'input_cache_write',
  'output_non_reasoning',
  'output_reasoning',
  'unclassified',
] as const satisfies ReadonlyArray<keyof Tokens>;

export type TokenColumn = (typeof TOKEN_COLUMNS)[number];

export const emptyTokens = (): Tokens => ({
  input_uncached: 0,
  input_cache_read: 0,
  input_cache_write: 0,
  output_non_reasoning: 0,
  output_reasoning: 0,
  unclassified: 0,
  total: 0,
});

export const addTokens = (into: Tokens, add: Tokens): Tokens => {
  for (const key of Object.keys(into) as (keyof Tokens)[]) into[key] += add[key] ?? 0;
  return into;
};

/** part over whole, or null when whole is zero, so an absent share never reads as 0%. */
export const share = (part: number, whole: number): number | null =>
  whole > 0 ? part / whole : null;

/** Cache-read tokens as a share of all input tokens. */
export const cacheShare = (t: Tokens): number | null =>
  share(t.input_cache_read, t.input_uncached + t.input_cache_read + t.input_cache_write);

/** Reasoning tokens as a share of all output tokens. */
export const reasoningShare = (t: Tokens): number | null =>
  share(t.output_reasoning, t.output_non_reasoning + t.output_reasoning);

/** Sums buckets' tokens and generations by a key; buckets the key maps to '' group as ''. */
export const groupBuckets = (
  buckets: Bucket[],
  key: (b: Bucket) => string
): Map<string, { generations: number; tokens: Tokens }> => {
  const out = new Map<string, { generations: number; tokens: Tokens }>();
  for (const b of buckets) {
    const k = key(b);
    const entry = out.get(k) ?? { generations: 0, tokens: emptyTokens() };
    entry.generations += b.generations;
    addTokens(entry.tokens, b.tokens);
    out.set(k, entry);
  }
  return out;
};

/** One row per day with each token column summed, for a stacked daily chart. */
export const dailyTokenRows = (buckets: Bucket[]): Array<{ day: string } & Tokens> =>
  [...groupBuckets(buckets, (b) => b.day).entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([day, entry]) => ({ day, ...entry.tokens }));

/** Shading from 0 to 1 for a value as its share of the largest one. */
export const intensity = (value: number, max: number): number =>
  max > 0 && value > 0 ? value / max : 0;

export interface StreamEvent {
  event?: string;
  data: string;
}

export type PayloadView =
  | { kind: 'json'; text: string }
  | { kind: 'events'; events: StreamEvent[] }
  | { kind: 'text'; text: string };

const prettyJSON = (text: string): string | null => {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return null;
  }
};

/** Server-sent events, one per blank-line-separated block. */
export const parseEventStream = (text: string): StreamEvent[] =>
  text
    .replace(/\r\n/g, '\n')
    .split(/\n\n+/)
    .flatMap((block) => {
      let event: string | undefined;
      const data: string[] = [];
      for (const line of block.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim();
        else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
      }
      return event === undefined && data.length === 0 ? [] : [{ event, data: data.join('\n') }];
    });

/**
 * How to show a payload: a JSON body pretty-printed, a stream as its event
 * sequence, one event or frame per line, and anything else as text.
 */
export const viewPayload = (body: string, contentType: string): PayloadView => {
  const trimmed = body.trim();
  if (/text\/event-stream/i.test(contentType) || /^(event|data):/m.test(trimmed.slice(0, 200))) {
    const events = parseEventStream(body);
    if (events.length > 0) return { kind: 'events', events };
  }
  const pretty = prettyJSON(trimmed);
  if (pretty !== null) return { kind: 'json', text: pretty };
  const lines = trimmed.split('\n').filter((line) => line.trim() !== '');
  if (lines.length > 1 && lines.every((line) => prettyJSON(line) !== null)) {
    return { kind: 'events', events: lines.map((line) => ({ data: line })) };
  }
  return { kind: 'text', text: body };
};

/** A stream event's data pretty-printed when it is JSON. */
export const prettyEventData = (data: string): string => prettyJSON(data) ?? data;

import type { ClientKey } from '@/services/api/clientKeys';

/** Visible ASCII, the characters a bearer token can carry unquoted. */
const KEY_PATTERN = /^[\x21-\x7E]+$/;

/** The server's limit on a client key's name, in characters. */
export const MAX_NAME_LENGTH = 64;

/**
 * The problem with a key entered in the editor, as a translation key, or
 * null. `stored` is the masked key being edited: sending it back unchanged
 * keeps the stored key. Any other key may not start with `*`, which the
 * server reads as a masked stored key.
 */
export const clientKeyProblem = (key: string, stored?: string): string | null => {
  if (stored !== undefined && key === stored) return null;
  if (!key) return 'client_keys.error_empty';
  if (!KEY_PATTERN.test(key)) return 'client_keys.error_charset';
  if (key.startsWith('*')) return 'client_keys.error_masked';
  return null;
};

export const nameProblem = (name: string): string | null =>
  [...name].length > MAX_NAME_LENGTH ? 'client_keys.error_name_long' : null;

/** Replace the key at index, or append it when index is null. */
export const withClientKey = (keys: ClientKey[], index: number | null, item: ClientKey) =>
  index === null ? [...keys, item] : keys.map((current, i) => (i === index ? item : current));

export const withoutClientKey = (keys: ClientKey[], index: number) =>
  keys.filter((_, i) => i !== index);

/** The gap between two strength segments lighting up in turn. */
export const SEGMENT_STAGGER_MS = 45;

/**
 * The delay before a strength segment lights: only segments this change
 * lights wait their turn, so generating a key lights all four as a wave while
 * typing one more character, or clearing segments, happens at once.
 */
export function segmentFillDelayMs(
  index: number,
  segments: number,
  previousSegments: number
): number {
  const filled = index < segments;
  if (!filled || index < previousSegments) return 0;
  return (index - Math.max(previousSegments, 0)) * SEGMENT_STAGGER_MS;
}

import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import {
  MAX_NAME_LENGTH,
  SEGMENT_STAGGER_MS,
  clientKeyProblem,
  nameProblem,
  segmentFillDelayMs,
  withClientKey,
  withoutClientKey,
} from '@/features/clientKeys/clientKeyForm';
import { StrengthMeter } from '@/features/clientKeys/StrengthMeter';
import { parseClientKeys } from '@/services/api/clientKeys';
import { generateSecureApiKey } from '@/utils/apiKey';

describe('client key form', () => {
  test('accepts generated keys and reports the first problem of others', () => {
    expect(clientKeyProblem(generateSecureApiKey())).toBeNull();
    expect(clientKeyProblem('')).toBe('client_keys.error_empty');
    expect(clientKeyProblem('has space')).toBe('client_keys.error_charset');
    expect(clientKeyProblem('ключ')).toBe('client_keys.error_charset');
    expect(clientKeyProblem('****1234')).toBe('client_keys.error_masked');
  });

  test('an edited key may come back as the masked key it was', () => {
    expect(clientKeyProblem('****1234', '****1234')).toBeNull();
    expect(clientKeyProblem('****5678', '****1234')).toBe('client_keys.error_masked');
  });

  test('names are optional and bounded in characters', () => {
    expect(nameProblem('')).toBeNull();
    expect(nameProblem('ж'.repeat(MAX_NAME_LENGTH))).toBeNull();
    expect(nameProblem('n'.repeat(MAX_NAME_LENGTH + 1))).toBe('client_keys.error_name_long');
  });

  test('edits the list by position and pairs keys with their names', () => {
    const a = { key: '****aaaa', name: 'a' };
    const b = { key: '****bbbb', name: '' };
    const c = { key: 'new-key', name: 'c' };
    expect(withClientKey([a, b], null, c)).toEqual([a, b, c]);
    expect(withClientKey([a, b], 0, c)).toEqual([c, b]);
    expect(withoutClientKey([a, b, c], 1)).toEqual([a, c]);
    expect(
      parseClientKeys({ 'api-keys': ['****aaaa', '****bbbb'], 'api-key-names': ['a'] })
    ).toEqual([a, b]);
    expect(parseClientKeys(undefined)).toEqual([]);
  });
});

describe('strength meter', () => {
  test('exposes the tier through the progressbar', () => {
    const markup = renderToStaticMarkup(
      createElement(StrengthMeter, { value: generateSecureApiKey() })
    );
    expect(markup).toContain('aria-valuenow="4"');
    expect(markup).toContain(`aria-valuetext="${i18n.t('client_keys.strength_strong')}"`);
    expect(markup.match(/data-filled="true"/g)).toHaveLength(4);
  });

  test('empty input lights nothing and shows a placeholder label', () => {
    const markup = renderToStaticMarkup(createElement(StrengthMeter, { value: '' }));
    expect(markup).toContain('aria-valuenow="0"');
    expect(markup).not.toContain('data-filled="true"');
    expect(markup).toContain('—');
  });

  test('segments cascade only over the newly lit ones', () => {
    const delays = (segments: number, previous: number) =>
      [0, 1, 2, 3].map((index) => segmentFillDelayMs(index, segments, previous));
    expect(delays(4, 0)).toEqual([
      0,
      SEGMENT_STAGGER_MS,
      SEGMENT_STAGGER_MS * 2,
      SEGMENT_STAGGER_MS * 3,
    ]);
    expect(delays(3, 2)).toEqual([0, 0, 0, 0]);
    expect(delays(3, 1)).toEqual([0, 0, SEGMENT_STAGGER_MS, 0]);
    expect(delays(2, 4)).toEqual([0, 0, 0, 0]);
  });

  test('every tier label has English text', () => {
    for (const key of [
      'strength',
      'strength_empty',
      'strength_weak',
      'strength_fair',
      'strength_good',
      'strength_strong',
    ]) {
      expect(i18n.exists(`client_keys.${key}`)).toBe(true);
    }
  });
});

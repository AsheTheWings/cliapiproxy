import { describe, expect, test } from 'bun:test';
import { formatFileSize } from '../src/utils/format';

describe('formatFileSize', () => {
  test('shows sizes under one kilobyte as whole bytes', () => {
    expect(formatFileSize(224)).toBe('224 B');
  });

  test('shows larger sizes with two decimals', () => {
    expect(formatFileSize(1536)).toBe('1.50 KB');
  });
});

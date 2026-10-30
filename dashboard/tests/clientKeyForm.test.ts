import { describe, expect, test } from 'bun:test';
import { clientKeyProblem } from '@/features/clientKeys/clientKeyForm';
import { generateSecureApiKey } from '@/utils/apiKey';

describe('client key form', () => {
  test('accepts generated keys and reports the first problem of others', () => {
    expect(clientKeyProblem(generateSecureApiKey())).toBeNull();
    expect(clientKeyProblem('')).toBe('client_keys.error_empty');
    expect(clientKeyProblem('has space')).toBe('client_keys.error_charset');
    expect(clientKeyProblem('ключ')).toBe('client_keys.error_charset');
    expect(clientKeyProblem('****1234')).toBe('client_keys.error_masked');
  });
});

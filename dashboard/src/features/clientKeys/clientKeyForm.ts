/** Visible ASCII, the characters a bearer token can carry unquoted. */
const KEY_PATTERN = /^[\x21-\x7E]+$/;

/**
 * The problem with a new client key, as a translation key, or null. A key
 * may not start with `*`, which the server reads as a masked stored key.
 */
export const clientKeyProblem = (key: string): string | null => {
  if (!key) return 'client_keys.error_empty';
  if (!KEY_PATTERN.test(key)) return 'client_keys.error_charset';
  if (key.startsWith('*')) return 'client_keys.error_masked';
  return null;
};

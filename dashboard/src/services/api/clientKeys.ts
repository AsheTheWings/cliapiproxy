/**
 * Client API keys and their names, as GET /config shows them under
 * access.api-keys and access.api-key-names, and as
 * PUT /config/access/api-keys takes them back.
 */
import { apiClient } from './client';
import { isRecord } from '@/utils/helpers';

export interface ClientKey {
  /** Masked as `****` plus the last four characters, or `****` for short keys. */
  key: string;
  /** Empty for an unnamed key. */
  name: string;
}

/** Read the client keys and their names from the access subtree. */
export const parseClientKeys = (access: unknown): ClientKey[] => {
  const source = isRecord(access) ? access : {};
  const keys = Array.isArray(source['api-keys']) ? source['api-keys'] : [];
  const names = Array.isArray(source['api-key-names']) ? source['api-key-names'] : [];
  return keys.flatMap((key, index) =>
    typeof key === 'string'
      ? [{ key, name: typeof names[index] === 'string' ? names[index] : '' }]
      : []
  );
};

export const clientKeysApi = {
  /** Replace the whole list; a masked key keeps the stored key it masks. */
  replace: (keys: ClientKey[]) =>
    apiClient.put(
      '/config/access/api-keys',
      keys.map((item) => ({ 'api-key': item.key, name: item.name }))
    ),
};

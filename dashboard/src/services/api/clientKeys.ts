/**
 * Client API keys and their labels, as GET /config shows them under
 * access.api-keys and as PUT /config/access/api-keys takes them back.
 */
import { apiClient } from './client';
import { isRecord } from '@/utils/helpers';

export interface ClientKey {
  /** Masked as `****` plus the last four characters, or `****` for short keys. */
  key: string;
  /** Empty for an unlabeled key. */
  label: string;
  /** The first 12 hexadecimal characters of the key's SHA-256; empty for a key not yet saved. */
  fingerprint: string;
}

/** Read the client keys with their labels and fingerprints from the access subtree. */
export const parseClientKeys = (access: unknown): ClientKey[] => {
  const source = isRecord(access) ? access : {};
  const keys = Array.isArray(source['api-keys']) ? source['api-keys'] : [];
  return keys.flatMap((item) =>
    isRecord(item) && typeof item['api-key'] === 'string'
      ? [
          {
            key: item['api-key'],
            label: typeof item.label === 'string' ? item.label : '',
            fingerprint: typeof item.fingerprint === 'string' ? item.fingerprint : '',
          },
        ]
      : []
  );
};

/** A client key's name where it has no label: its fingerprint. */
export const clientKeyDisplay = (item: ClientKey): string => item.label || item.fingerprint;

export const clientKeysApi = {
  /** Replace the whole list; a masked key keeps the stored key it masks. */
  replace: (keys: ClientKey[]) =>
    apiClient.put(
      '/config/access/api-keys',
      keys.map((item) => ({ 'api-key': item.key, ...(item.label ? { label: item.label } : {}) }))
    ),
};

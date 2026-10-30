/**
 * Client API keys, as GET /config shows them under access.api-keys and
 * PUT /config/access/api-keys takes them back.
 */
import { apiClient } from './client';

export const clientKeysApi = {
  /** Replace the whole list; a masked key keeps the stored key it masks. */
  replace: (keys: string[]) => apiClient.put('/config/access/api-keys', keys),
};

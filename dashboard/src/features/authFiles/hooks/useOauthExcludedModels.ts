import { useCallback, useEffect, useRef, useState } from 'react';
import { authFilesApi } from '@/services/api';

export type UseOauthExcludedModelsResult = {
  excluded: Record<string, string[]>;
  loadExcluded: () => Promise<void>;
};

/**
 * Reads the configured OAuth excluded models by provider, which the credential
 * model list marks. A failed read keeps the last map loaded.
 */
export function useOauthExcludedModels(): UseOauthExcludedModelsResult {
  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const requestRef = useRef(0);

  useEffect(
    () => () => {
      requestRef.current += 1;
    },
    []
  );

  const loadExcluded = useCallback(async () => {
    const requestId = ++requestRef.current;
    try {
      const res = await authFilesApi.getOauthExcludedModels();
      if (requestId === requestRef.current) setExcluded(res);
    } catch {
      // The list keeps its last map; the page's refresh reads it again.
    }
  }, []);

  return { excluded, loadExcluded };
}

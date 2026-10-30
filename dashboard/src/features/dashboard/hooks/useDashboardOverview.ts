import { useCallback, useEffect, useMemo, useState } from 'react';
import { authFilesApi } from '@/services/api';
import { useAuthStore, useConfigStore } from '@/stores';
import {
  PROVIDER_FAMILIES,
  parseProviderGroups,
  type ProviderFamily,
} from '@/services/api/providerGroups';
import {
  mergeRecentRequestBucketGroups,
  normalizeRecentRequestUsageEntry,
  type RecentRequestBucket,
} from '@/utils/recentRequests';
import type { Config } from '@/types';
import type { AuthFileItem } from '@/types/authFile';
import {
  TRAFFIC_BUCKET_MINUTES,
  type CredentialHealth,
  type DashboardCounts,
  type ProviderTraffic,
  type TrafficWindow,
} from '../types';

const EMPTY_TRAFFIC: TrafficWindow = {
  buckets: [],
  totalSuccess: 0,
  totalFailure: 0,
  total: 0,
  successRate: null,
  peakTotal: 0,
  peakIndex: -1,
  windowMinutes: 0,
};

const providerIdOfAuthFile = (file: AuthFileItem): string => {
  const candidate = String(file.type ?? file.provider ?? '')
    .trim()
    .toLowerCase();
  return candidate && candidate !== 'empty' ? candidate : 'unknown';
};

const buildTrafficWindow = (bucketGroups: RecentRequestBucket[][]): TrafficWindow => {
  const buckets = mergeRecentRequestBucketGroups(bucketGroups);
  if (buckets.length === 0) {
    return EMPTY_TRAFFIC;
  }

  let totalSuccess = 0;
  let totalFailure = 0;
  let peakTotal = 0;
  let peakIndex = -1;

  buckets.forEach((bucket, index) => {
    const bucketTotal = bucket.success + bucket.failed;
    totalSuccess += bucket.success;
    totalFailure += bucket.failed;
    if (bucketTotal > peakTotal) {
      peakTotal = bucketTotal;
      peakIndex = index;
    }
  });

  const total = totalSuccess + totalFailure;

  return {
    buckets,
    totalSuccess,
    totalFailure,
    total,
    successRate: total > 0 ? (totalSuccess / total) * 100 : null,
    peakTotal,
    peakIndex,
    windowMinutes: buckets.length * TRAFFIC_BUCKET_MINUTES,
  };
};

interface ProviderAccumulator {
  credentials: number;
  success: number;
  failure: number;
  bucketGroups: RecentRequestBucket[][];
}

const createAccumulator = (): ProviderAccumulator => ({
  credentials: 0,
  success: 0,
  failure: 0,
  bucketGroups: [],
});

/** Upstream API keys per family, across every group of the key state. */
export const getProviderKeyCounts = (config: Config): Record<ProviderFamily, number> => {
  const groups = parseProviderGroups(config.raw?.['api-keys']);
  return Object.fromEntries(
    PROVIDER_FAMILIES.map((family) => [
      family,
      groups[family].reduce((sum, group) => sum + group.keys.length, 0),
    ])
  ) as Record<ProviderFamily, number>;
};

/**
 * 汇总仪表盘所需的全部数据。
 *
 * 流量数据来自 `credentials` 列表中每个凭证的近期请求。
 */
export function useDashboardOverview() {
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);

  const connected = connectionStatus === 'connected';

  const [authFiles, setAuthFiles] = useState<AuthFileItem[] | null>(null);

  const loadAuthFiles = useCallback(async () => {
    if (!connected) return;
    try {
      const response = await authFilesApi.list();
      setAuthFiles(response.files);
    } catch {
      setAuthFiles(null);
    }
  }, [connected]);

  useEffect(() => {
    if (!connected) return;
    void fetchConfig().catch(() => undefined);
    void loadAuthFiles();
  }, [connected, fetchConfig, loadAuthFiles]);

  const refresh = useCallback(async () => {
    if (!connected) return;
    await Promise.allSettled([fetchConfig(true), loadAuthFiles()]);
  }, [connected, fetchConfig, loadAuthFiles]);

  const providerKeyCounts = useMemo(() => (config ? getProviderKeyCounts(config) : null), [config]);

  const { traffic, providers } = useMemo(() => {
    const accumulators = new Map<string, ProviderAccumulator>();
    const allBucketGroups: RecentRequestBucket[][] = [];

    const accumulatorFor = (providerId: string): ProviderAccumulator => {
      const existing = accumulators.get(providerId);
      if (existing) return existing;
      const created = createAccumulator();
      accumulators.set(providerId, created);
      return created;
    };

    (authFiles ?? []).forEach((file) => {
      const accumulator = accumulatorFor(providerIdOfAuthFile(file));
      const entry = normalizeRecentRequestUsageEntry(file);
      accumulator.credentials += 1;
      accumulator.success += entry.success;
      accumulator.failure += entry.failed;
      if (entry.recentRequests.length > 0) {
        accumulator.bucketGroups.push(entry.recentRequests);
        allBucketGroups.push(entry.recentRequests);
      }
    });

    const providerRows: ProviderTraffic[] = Array.from(accumulators.entries())
      .map(([id, accumulator]) => {
        const total = accumulator.success + accumulator.failure;
        return {
          id,
          credentials: accumulator.credentials,
          success: accumulator.success,
          failure: accumulator.failure,
          total,
          successRate: total > 0 ? (accumulator.success / total) * 100 : null,
          buckets: mergeRecentRequestBucketGroups(accumulator.bucketGroups),
        };
      })
      .sort(
        (a, b) => b.total - a.total || b.credentials - a.credentials || a.id.localeCompare(b.id)
      );

    return {
      traffic: buildTrafficWindow(allBucketGroups),
      providers: providerRows,
    };
  }, [authFiles]);

  const credentials = useMemo<CredentialHealth | null>(() => {
    if (!authFiles) return null;

    let disabled = 0;
    let unavailable = 0;
    const countsByType = new Map<string, number>();

    authFiles.forEach((file) => {
      if (file.disabled) {
        disabled += 1;
      } else if (file.unavailable) {
        unavailable += 1;
      }
      const type = providerIdOfAuthFile(file);
      countsByType.set(type, (countsByType.get(type) ?? 0) + 1);
    });

    return {
      total: authFiles.length,
      active: authFiles.length - disabled - unavailable,
      disabled,
      unavailable,
      byType: Array.from(countsByType.entries())
        .map(([type, count]) => ({ type, count }))
        .sort((a, b) => b.count - a.count || a.type.localeCompare(b.type)),
    };
  }, [authFiles]);

  const counts = useMemo<DashboardCounts>(
    () => ({
      clientKeys: config ? (config.apiKeys?.length ?? 0) : null,
      providerKeys: providerKeyCounts
        ? Object.values(providerKeyCounts).reduce((sum, count) => sum + count, 0)
        : null,
      credentials: authFiles ? authFiles.length : null,
    }),
    [config, providerKeyCounts, authFiles]
  );

  return {
    connectionStatus,
    connected,
    config,
    counts,
    providerKeyCounts,
    traffic,
    providers,
    credentials,
    refresh,
  };
}

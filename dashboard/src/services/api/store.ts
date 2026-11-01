/**
 * The quota and generations routes, which read the server's store.
 */
import { apiClient } from './client';

export interface Meter {
  meter: string;
  scope?: string;
  unit: 'credits' | 'percent' | 'usd' | 'requests' | string;
  used: number | null;
  limit: number | null;
  remaining: number | null;
  window_seconds?: number;
  resets_at?: string;
}

export interface UsageSnapshot {
  snapshot_id: number;
  observed_at: string;
  source: 'poll' | 'response_headers' | string;
  plan?: string;
  meters: Meter[];
}

export interface CredentialUsage {
  credential_id: string;
  provider: string;
  provider_group?: string;
  account_label?: string;
  configured: boolean;
  usage_source: boolean;
  latest: UsageSnapshot | null;
}

export interface Tokens {
  input_uncached: number;
  input_cache_read: number;
  input_cache_write: number;
  output_non_reasoning: number;
  output_reasoning: number;
  unclassified: number;
  total: number;
}

export interface TimelinePoint {
  snapshot_id: number;
  observed_at: string;
  source: string;
  plan?: string;
  meter: Meter;
  previous_observed_at?: string;
  used_change?: number;
  remaining_change?: number;
  generations?: number;
  tokens?: Tokens;
}

export interface Generation {
  generation_id: string;
  time: string;
  trace_id?: string;
  trace_url?: string;
  conversation_id?: string;
  status: string;
  client_key_label?: string;
  client_key_fingerprint?: string;
  client_user_agent?: string;
  provider: string;
  provider_group?: string;
  account_label?: string;
  account_scope?: string;
  credential_id?: string;
  key_fingerprint?: string;
  auth_type?: string;
  executor?: string;
  requested_model?: string;
  upstream_model?: string;
  response_model?: string;
  reasoning_effort?: string;
  requested_service_tier?: string;
  effective_service_tier?: string;
  response_service_tier?: string;
  stream: boolean;
  tokens: Tokens;
  token_quality: string;
  token_source: string;
  usage_amount?: number;
  usage_unit?: string;
  duration_ms?: number;
  first_token_ms?: number;
  system_instruction_hash?: string;
  prior_snapshot_id?: number;
  response_snapshot_id?: number;
}

export interface PayloadInfo {
  kind: PayloadKind;
  content_type: string;
  original_bytes: number;
  stored_bytes: number;
  truncated: boolean;
  pruned: boolean;
}

export interface GenerationDetail extends Generation {
  usage_raw?: unknown;
  prior_snapshot: UsageSnapshot | null;
  response_snapshot: UsageSnapshot | null;
  payloads: PayloadInfo[];
}

export type PayloadKind = 'client_input' | 'upstream_input' | 'upstream_response';

export interface GenerationFilter {
  from?: string;
  to?: string;
  provider?: string;
  credential?: string;
  model?: string;
  effort?: string;
  client?: string;
  conversation?: string;
  status?: string;
  limit?: number;
  cursor?: string;
}

export interface GenerationPage {
  generations: Generation[];
  next_cursor?: string;
}

export type SummaryDimension = 'credential' | 'model' | 'effort' | 'client';

export interface Bucket {
  day: string;
  credential_id?: string;
  provider?: string;
  provider_group?: string;
  account_label?: string;
  upstream_model?: string;
  reasoning_effort?: string;
  client_key_fingerprint?: string;
  client_key_label?: string;
  generations: number;
  tokens: Tokens;
  usage_credits?: number;
  usage_usd?: number;
}

export interface LatencyPoint {
  day: string;
  upstream_model: string;
  generations: number;
  p50_ms: number;
  p90_ms: number;
  p99_ms: number;
}

export interface Payload {
  body: string;
  contentType: string;
  rebuilt: boolean;
  truncated: boolean;
}

/** Query parameters without the empty ones. */
const params = (values: object): Record<string, string> =>
  Object.fromEntries(
    Object.entries(values)
      .filter(([, value]) => value !== undefined && value !== '')
      .map(([key, value]) => [key, String(value)])
  );

export const storeApi = {
  quota: () =>
    apiClient.get<{ credentials: CredentialUsage[] }>('/quota').then((r) => r.credentials),

  refreshQuota: (credentialId: string) =>
    apiClient.post<{ outcome: string; credential: CredentialUsage }>('/quota/refresh', {
      credential_id: credentialId,
    }),

  timeline: (credentialId: string, from?: string, to?: string) =>
    apiClient
      .get<{ points: TimelinePoint[] }>('/quota/timeline', {
        params: params({ credential: credentialId, from, to }),
      })
      .then((r) => r.points),

  generations: (filter: GenerationFilter) =>
    apiClient.get<GenerationPage>('/generations', { params: params(filter) }),

  generation: (id: string) =>
    apiClient.get<GenerationDetail>(`/generations/${encodeURIComponent(id)}`),

  payload: async (id: string, kind: PayloadKind): Promise<Payload> => {
    const response = await apiClient.getRaw(
      `/generations/${encodeURIComponent(id)}/payloads/${kind}`,
      { responseType: 'text', transformResponse: (data: unknown) => data }
    );
    const header = (name: string) => String(response.headers[name.toLowerCase()] ?? '');
    return {
      body: typeof response.data === 'string' ? response.data : String(response.data ?? ''),
      contentType: header('Content-Type'),
      rebuilt: header('X-Payload-Rebuilt') === 'true',
      truncated: header('X-Payload-Truncated') === 'true',
    };
  },

  summary: (from: string, to: string, groupBy: SummaryDimension[]) =>
    apiClient
      .get<{ buckets: Bucket[] }>('/generations/summary', {
        params: params({ from, to, group_by: groupBy.join(',') }),
      })
      .then((r) => r.buckets),

  latency: (from: string, to: string) =>
    apiClient
      .get<{ latency: LatencyPoint[] }>('/generations/latency', { params: params({ from, to }) })
      .then((r) => r.latency),
};

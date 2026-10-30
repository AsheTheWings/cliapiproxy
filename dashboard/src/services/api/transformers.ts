import type { Config } from '@/types/config';
import { isRecord } from '@/utils/helpers';

const normalizeBoolean = (value: unknown): boolean | undefined =>
  typeof value === 'boolean' ? value : undefined;

/** Read the fields the dashboard shows from the v8 configuration view. */
export const normalizeConfigResponse = (raw: unknown): Config => {
  const config: Config = { raw: isRecord(raw) ? raw : {} };
  if (!isRecord(raw)) return config;
  const at = (path: string): unknown =>
    path
      .split('.')
      .reduce<unknown>((value, key) => (isRecord(value) ? value[key] : undefined), raw);
  config.debug = normalizeBoolean(at('observability.logs.debug'));
  config.requestLog = normalizeBoolean(at('observability.logs.request-log'));
  config.loggingToFile = normalizeBoolean(at('observability.logs.logging-to-file'));
  const retry = at('routing.retry.request-retry');
  if (typeof retry === 'number') config.requestRetry = retry;
  config.wsAuth = normalizeBoolean(at('oauth.providers.aistudio.ws-auth')) ?? true;
  config.forceModelPrefix = normalizeBoolean(at('routing.force-model-prefix'));
  const strategy = at('routing.strategy');
  if (typeof strategy === 'string') config.routingStrategy = strategy;
  const keys = at('access.api-keys');
  config.apiKeys = Array.isArray(keys)
    ? keys.filter((key): key is string => typeof key === 'string')
    : [];
  return config;
};

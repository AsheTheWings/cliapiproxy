/**
 * Upstream groups of the API-key families, as GET /config shows them under
 * api-keys and PUT /config/api-keys/<family> takes them back.
 */
import { apiClient } from './client';
import { isRecord } from '@/utils/helpers';

export const PROVIDER_FAMILIES = ['openai-compatibility', 'meta', 'commandcode', 'kiro'] as const;
export type ProviderFamily = (typeof PROVIDER_FAMILIES)[number];

export interface UpstreamModel {
  name: string;
  alias?: string;
  'display-name'?: string;
  'max-context-length'?: number;
  'input-modalities'?: string[];
  thinking?: { levels: string[] };
}

export interface UpstreamKey {
  /** Masked as `****` plus the last four characters, or `****` for short keys. */
  'api-key': string;
  authIndex?: string;
}

export interface UpstreamGroup {
  name: string;
  'base-url'?: string;
  prefix?: string;
  models?: UpstreamModel[];
  keys: UpstreamKey[];
}

export type ProviderGroups = Record<ProviderFamily, UpstreamGroup[]>;

const text = (value: unknown): string | undefined =>
  typeof value === 'string' && value.trim() ? value : undefined;

const strings = (value: unknown): string[] | undefined =>
  Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : undefined;

const parseModel = (raw: unknown): UpstreamModel | null => {
  if (!isRecord(raw) || !text(raw.name)) return null;
  const model: UpstreamModel = { name: String(raw.name) };
  if (text(raw.alias)) model.alias = String(raw.alias);
  if (text(raw['display-name'])) model['display-name'] = String(raw['display-name']);
  if (typeof raw['max-context-length'] === 'number')
    model['max-context-length'] = raw['max-context-length'];
  const modalities = strings(raw['input-modalities']);
  if (modalities?.length) model['input-modalities'] = modalities;
  const levels = isRecord(raw.thinking) ? strings(raw.thinking.levels) : undefined;
  if (levels?.length) model.thinking = { levels };
  return model;
};

const parseGroup = (raw: unknown): UpstreamGroup | null => {
  if (!isRecord(raw) || !text(raw.name) || !Array.isArray(raw.keys)) return null;
  const group: UpstreamGroup = {
    name: String(raw.name),
    keys: raw.keys.flatMap((key) => {
      if (!isRecord(key) || typeof key['api-key'] !== 'string') return [];
      const index = text(key.auth_index) ?? text(key['auth-index']);
      return [{ 'api-key': key['api-key'], ...(index ? { authIndex: index } : {}) }];
    }),
  };
  if (text(raw['base-url'])) group['base-url'] = String(raw['base-url']);
  if (text(raw.prefix)) group.prefix = String(raw.prefix);
  if (Array.isArray(raw.models)) {
    group.models = raw.models.flatMap((model) => parseModel(model) ?? []);
  }
  return group;
};

/** Read each family's groups from the api-keys subtree of the configuration. */
export const parseProviderGroups = (apiKeys: unknown): ProviderGroups => {
  const source = isRecord(apiKeys) ? apiKeys : {};
  return Object.fromEntries(
    PROVIDER_FAMILIES.map((family) => [
      family,
      Array.isArray(source[family])
        ? source[family].flatMap((group: unknown) => parseGroup(group) ?? [])
        : [],
    ])
  ) as ProviderGroups;
};

/** The body PUT takes: the group without the view's auth indexes. */
export const serializeGroup = (group: UpstreamGroup): Record<string, unknown> => {
  const body: Record<string, unknown> = { name: group.name };
  if (group['base-url']) body['base-url'] = group['base-url'];
  if (group.prefix) body.prefix = group.prefix;
  if (group.models?.length) body.models = group.models;
  body.keys = group.keys.map((key) => ({ 'api-key': key['api-key'] }));
  return body;
};

export const providerGroupsApi = {
  /** Replace a family's whole list; masked keys keep the stored keys they mask. */
  replace: (family: ProviderFamily, groups: UpstreamGroup[]) =>
    apiClient.put(`/config/api-keys/${family}`, groups.map(serializeGroup)),
};

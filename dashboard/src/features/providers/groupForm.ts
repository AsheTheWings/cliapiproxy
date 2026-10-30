/**
 * The editable form of an upstream group and its conversion to and from the
 * server's group. React-free, so tests use it directly.
 */
import type { UpstreamGroup, UpstreamModel } from '@/services/api/providerGroups';
import type { FamilyDescriptor } from './families';

export interface ModelDraft {
  name: string;
  alias: string;
  displayName: string;
  maxContextLength: string;
  inputModalities: string;
  thinkingLevels: string[];
}

export interface KeyDraft {
  /** A stored key as the server shows it, masked; sent back unchanged it keeps the key. */
  stored?: string;
  /** A key entered in this form. */
  value: string;
}

export interface GroupDraft {
  name: string;
  baseUrl: string;
  prefix: string;
  models: ModelDraft[];
  keys: KeyDraft[];
}

export interface DraftError {
  key: string;
  values?: Record<string, string | number>;
}

/** The server's form of group names and prefixes. */
export const NAME_PATTERN = /^[a-z0-9][a-z0-9-]*$/;

export const emptyModel = (): ModelDraft => ({
  name: '',
  alias: '',
  displayName: '',
  maxContextLength: '',
  inputModalities: '',
  thinkingLevels: [],
});

export const emptyDraft = (): GroupDraft => ({
  name: '',
  baseUrl: '',
  prefix: '',
  models: [],
  keys: [{ value: '' }],
});

export const toDraft = (group: UpstreamGroup): GroupDraft => ({
  name: group.name,
  baseUrl: group['base-url'] ?? '',
  prefix: group.prefix ?? '',
  models: (group.models ?? []).map((model) => ({
    name: model.name,
    alias: model.alias ?? '',
    displayName: model['display-name'] ?? '',
    maxContextLength:
      model['max-context-length'] === undefined ? '' : String(model['max-context-length']),
    inputModalities: (model['input-modalities'] ?? []).join(', '),
    thinkingLevels: model.thinking?.levels ?? [],
  })),
  keys: group.keys.map((key) => ({ stored: key['api-key'], value: '' })),
});

const splitList = (value: string): string[] =>
  value
    .split(/[,\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);

const fromModel = (draft: ModelDraft, family: FamilyDescriptor): UpstreamModel => {
  const model: UpstreamModel = { name: draft.name.trim() };
  if (draft.alias.trim()) model.alias = draft.alias.trim();
  if (draft.displayName.trim()) model['display-name'] = draft.displayName.trim();
  if (draft.maxContextLength.trim()) model['max-context-length'] = Number(draft.maxContextLength);
  const modalities = splitList(draft.inputModalities);
  if (family.inputModalities && modalities.length) model['input-modalities'] = modalities;
  if (draft.thinkingLevels.length) model.thinking = { levels: [...draft.thinkingLevels] };
  return model;
};

/** Rows left blank are dropped rather than reported. */
const usedModels = (draft: GroupDraft) => draft.models.filter((model) => model.name.trim());
const usedKeys = (draft: GroupDraft) => draft.keys.filter((key) => key.stored || key.value.trim());

export const fromDraft = (draft: GroupDraft, family: FamilyDescriptor): UpstreamGroup => {
  const group: UpstreamGroup = {
    name: draft.name.trim(),
    keys: usedKeys(draft).map((key) => ({ 'api-key': key.stored ?? key.value.trim() })),
  };
  if (family.baseUrl !== 'none' && draft.baseUrl.trim()) group['base-url'] = draft.baseUrl.trim();
  if (draft.prefix.trim()) group.prefix = draft.prefix.trim();
  const models = usedModels(draft).map((model) => fromModel(model, family));
  if (models.length) group.models = models;
  return group;
};

const validBaseUrl = (value: string): boolean => {
  try {
    const url = new URL(value);
    return (
      (url.protocol === 'https:' || url.protocol === 'http:') &&
      Boolean(url.host) &&
      !url.username &&
      !url.password
    );
  } catch {
    return false;
  }
};

/**
 * The first problem that would make the server refuse the group, or null.
 * `takenNames` are the names of the family's other groups.
 */
export const validateDraft = (
  draft: GroupDraft,
  family: FamilyDescriptor,
  takenNames: string[]
): DraftError | null => {
  const name = draft.name.trim();
  if (!NAME_PATTERN.test(name)) return { key: 'provider_groups.error_name' };
  if (takenNames.includes(name))
    return { key: 'provider_groups.error_name_taken', values: { name } };
  const baseUrl = draft.baseUrl.trim();
  if (family.baseUrl === 'required' && !baseUrl)
    return { key: 'provider_groups.error_base_url_required' };
  if (family.baseUrl !== 'none' && baseUrl && !validBaseUrl(baseUrl))
    return { key: 'provider_groups.error_base_url' };
  const prefix = draft.prefix.trim();
  if (prefix && !NAME_PATTERN.test(prefix)) return { key: 'provider_groups.error_prefix' };
  const models = usedModels(draft);
  if (family.modelsRequired && models.length === 0)
    return { key: 'provider_groups.error_models_required' };
  for (const model of models) {
    const context = model.maxContextLength.trim();
    if (context && !/^\d+$/.test(context))
      return { key: 'provider_groups.error_context', values: { model: model.name.trim() } };
  }
  const keys = usedKeys(draft);
  if (keys.length === 0) return { key: 'provider_groups.error_keys_required' };
  const entered = keys.filter((key) => !key.stored).map((key) => key.value.trim());
  if (new Set(entered).size !== entered.length)
    return { key: 'provider_groups.error_key_repeated' };
  return null;
};

/** The family's list after putting `group` at `index`, or appending it when index is null. */
export const withGroup = (
  groups: UpstreamGroup[],
  index: number | null,
  group: UpstreamGroup
): UpstreamGroup[] =>
  index === null ? [...groups, group] : groups.map((item, i) => (i === index ? group : item));

export const withoutGroup = (groups: UpstreamGroup[], index: number): UpstreamGroup[] =>
  groups.filter((_, i) => i !== index);

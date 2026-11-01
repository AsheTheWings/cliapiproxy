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
  /** The stored key's fingerprint, shown beside it. */
  fingerprint?: string;
  /** A key entered in this form. */
  value: string;
  /** The key's label as entered; blank leaves the key unlabeled. */
  label: string;
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

/** The server's limit on a key's label, in characters. */
export const MAX_LABEL_LENGTH = 64;

/**
 * The problem with a label as entered, as a translation key, or null. The
 * server trims labels and refuses one longer than the limit or holding a
 * line break or other control character.
 */
export const labelProblem = (label: string): string | null => {
  const trimmed = label.trim();
  if ([...trimmed].length > MAX_LABEL_LENGTH) return 'labels.error_long';
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/.test(trimmed)) return 'labels.error_control';
  return null;
};

/**
 * The name a group's key shows under: its label, or for an unlabeled stored
 * key the group name followed by its fingerprint, as the server names the
 * key's account. A new key has no fingerprint until it is saved.
 */
export const keyAccountLabel = (group: string, key: KeyDraft): string =>
  key.label.trim() || (key.fingerprint ? `${group.trim()} ${key.fingerprint}` : '');

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
  keys: [{ value: '', label: '' }],
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
  keys: group.keys.map((key) => ({
    stored: key['api-key'],
    ...(key.fingerprint ? { fingerprint: key.fingerprint } : {}),
    value: '',
    label: key.label ?? '',
  })),
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
    keys: usedKeys(draft).map((key) => ({
      'api-key': key.stored ?? key.value.trim(),
      ...(key.label.trim() ? { label: key.label.trim() } : {}),
    })),
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
  for (const key of keys) {
    const problem = labelProblem(key.label);
    if (problem) return { key: problem, values: { max: MAX_LABEL_LENGTH } };
  }
  const labels = keys.map((key) => key.label.trim()).filter(Boolean);
  const repeated = labels.find((label, i) => labels.indexOf(label) !== i);
  if (repeated) return { key: 'labels.error_repeated', values: { label: repeated } };
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

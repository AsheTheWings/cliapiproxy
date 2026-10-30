import { describe, expect, test } from 'bun:test';
import { parseProviderGroups, serializeGroup } from '@/services/api/providerGroups';
import { familyDescriptor } from '@/features/providers/families';
import {
  emptyModel,
  emptyDraft,
  fromDraft,
  toDraft,
  validateDraft,
  withGroup,
  withoutGroup,
} from '@/features/providers/groupForm';

const view = {
  commandcode: [
    {
      name: 'main',
      models: [{ name: 'deepseek/v4', thinking: { levels: ['high'] } }],
      keys: [{ 'api-key': '****6789', auth_index: 'a1' }],
    },
    { name: 'team', prefix: 'cc', keys: [{ 'api-key': '****' }, { 'api-key': '****4321' }] },
  ],
  gemini: [{ name: 'ignored', keys: [{ 'api-key': 'x' }] }],
};

describe('provider groups view', () => {
  test('reads each family in order', () => {
    const groups = parseProviderGroups(view);
    expect(Object.keys(groups)).toEqual(['openai-compatibility', 'meta', 'commandcode', 'kiro']);
    expect(groups.commandcode.map((g) => g.name)).toEqual(['main', 'team']);
    expect(groups.commandcode[0].keys[0]).toEqual({ 'api-key': '****6789', authIndex: 'a1' });
    expect(parseProviderGroups(undefined).kiro).toEqual([]);
  });

  test('sends groups back with masked keys and without auth indexes', () => {
    expect(parseProviderGroups(view).commandcode.map(serializeGroup)).toEqual([
      {
        name: 'main',
        models: [{ name: 'deepseek/v4', thinking: { levels: ['high'] } }],
        keys: [{ 'api-key': '****6789' }],
      },
      { name: 'team', prefix: 'cc', keys: [{ 'api-key': '****' }, { 'api-key': '****4321' }] },
    ]);
  });
});

describe('group form', () => {
  const openai = familyDescriptor('openai-compatibility');
  const kiro = familyDescriptor('kiro');

  test('an unchanged draft keeps stored keys and every model field', () => {
    const group = {
      name: 'example',
      'base-url': 'https://api.example.com/v1',
      prefix: 'ex',
      models: [
        {
          name: 'vendor/model-1',
          alias: 'model-1',
          'display-name': 'Model 1',
          'max-context-length': 200000,
          'input-modalities': ['text', 'image'],
          thinking: { levels: ['low', 'custom-level'] },
        },
      ],
      keys: [{ 'api-key': '****abcd' }],
    };
    expect(fromDraft(toDraft(group), openai)).toEqual(group);
  });

  test('drops blank rows, new keys join stored ones, and families without a base URL drop it', () => {
    const draft = {
      ...emptyDraft(),
      name: 'kiro',
      baseUrl: 'https://ignored.example',
      keys: [{ stored: '****1234', value: '' }, { value: ' new-key ' }, { value: '' }],
      models: [emptyModel()],
    };
    expect(fromDraft(draft, kiro)).toEqual({
      name: 'kiro',
      keys: [{ 'api-key': '****1234' }, { 'api-key': 'new-key' }],
    });
  });

  test('reports what the server would refuse', () => {
    const valid = {
      ...emptyDraft(),
      name: 'example',
      baseUrl: 'https://api.example.com/v1',
      models: [{ ...emptyModel(), name: 'm' }],
      keys: [{ value: 'k1' }],
    };
    expect(validateDraft(valid, openai, [])).toBeNull();
    const cases: Array<[Partial<typeof valid>, string, string[]?]> = [
      [{ name: 'Example' }, 'provider_groups.error_name'],
      [{}, 'provider_groups.error_name_taken', ['example']],
      [{ baseUrl: '' }, 'provider_groups.error_base_url_required'],
      [{ baseUrl: 'https://user:pass@api.example.com' }, 'provider_groups.error_base_url'],
      [{ baseUrl: 'ftp://api.example.com' }, 'provider_groups.error_base_url'],
      [{ prefix: 'Bad Prefix' }, 'provider_groups.error_prefix'],
      [{ models: [] }, 'provider_groups.error_models_required'],
      [
        { models: [{ ...valid.models[0], maxContextLength: '1.5' }] },
        'provider_groups.error_context',
      ],
      [{ keys: [{ value: ' ' }] }, 'provider_groups.error_keys_required'],
      [{ keys: [{ value: 'k1' }, { value: 'k1' }] }, 'provider_groups.error_key_repeated'],
    ];
    for (const [patch, key, taken] of cases) {
      expect(validateDraft({ ...valid, ...patch }, openai, taken ?? [])?.key).toBe(key);
    }
    expect(validateDraft({ ...valid, baseUrl: '', models: [] }, kiro, [])).toBeNull();
  });

  test('edits a family list by position', () => {
    const a = { name: 'a', keys: [] };
    const b = { name: 'b', keys: [] };
    const c = { name: 'c', keys: [] };
    expect(withGroup([a, b], null, c)).toEqual([a, b, c]);
    expect(withGroup([a, b], 1, c)).toEqual([a, c]);
    expect(withoutGroup([a, b, c], 1)).toEqual([a, c]);
  });
});

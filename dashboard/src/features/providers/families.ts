import type { ProviderFamily } from '@/services/api/providerGroups';
import iconOpenaiLight from '@/assets/icons/openai-light.svg';
import iconOpenaiDark from '@/assets/icons/openai-dark.svg';
import iconMeta from '@/assets/icons/meta.svg';

/** What a family's groups must and may declare; the server applies the same rules. */
export interface FamilyDescriptor {
  id: ProviderFamily;
  label: string;
  /** A themed logo, or a monogram when the brand ships none. */
  icon: { light: string; dark: string } | { monogram: string };
  baseUrl: 'required' | 'optional' | 'none';
  modelsRequired: boolean;
  inputModalities: boolean;
}

export const FAMILIES: FamilyDescriptor[] = [
  {
    id: 'openai-compatibility',
    label: 'OpenAI-compatible',
    icon: { light: iconOpenaiLight, dark: iconOpenaiDark },
    baseUrl: 'required',
    modelsRequired: true,
    inputModalities: true,
  },
  {
    id: 'meta',
    label: 'Meta Muse',
    icon: { light: iconMeta, dark: iconMeta },
    baseUrl: 'optional',
    modelsRequired: false,
    inputModalities: false,
  },
  {
    id: 'commandcode',
    label: 'Command Code',
    icon: { monogram: 'CC' },
    baseUrl: 'none',
    modelsRequired: false,
    inputModalities: false,
  },
  {
    id: 'kiro',
    label: 'Kiro',
    icon: { monogram: 'K' },
    baseUrl: 'none',
    modelsRequired: false,
    inputModalities: false,
  },
];

export const familyDescriptor = (id: ProviderFamily): FamilyDescriptor =>
  FAMILIES.find((family) => family.id === id) ?? FAMILIES[0];

import type { ThinkingLevel } from './thinkingLevels';
import type { ModelOptionsInput } from './modelOptions';

/** The API-key families whose upstream groups the server serves. */
export type ProviderBrand = 'openaiCompatibility' | 'meta' | 'commandcode' | 'kiro';

/** One editable model row. */
export interface ModelEntryInput extends ModelOptionsInput {
  /** Only used by the OAuth alias editor. */
  fork?: boolean;
  sourceIndex?: number | null;
  name: string;
  alias?: string;
  priority?: number;
  image?: boolean;
  /** Original backend value, preserved until the standard-level selector is changed. */
  thinkingJson?: string;
  thinkingLevels?: ThinkingLevel[];
  thinkingLevelsTouched?: boolean;
}

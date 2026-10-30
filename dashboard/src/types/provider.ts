
export interface RequestScopedErrorRule {
  status?: number;
  match?: string[];
  matchRegex?: string[];
  action?: 'stop' | 'stop-and-cooldown' | 'continue' | 'continue-and-cooldown';
}

/**
 * AI 提供商相关类型
 * 基于原项目 src/modules/ai-providers.js
 */

export interface ModelAlias {
  /** Persisted model position; null marks a new form row. Never sent to the backend. */
  sourceIndex?: number | null;
  name: string;
  alias?: string;
  priority?: number;
  image?: boolean;
  displayName?: string;
  maxContextLength?: number;
  forceMapping?: boolean;
  isCompat?: boolean;
  supportConfigurationUpdate?: boolean;
  inputModalities?: string[];
  outputModalities?: string[];
  useMaxCompletionTokens?: boolean;
  thinking?: Record<string, unknown>;
}


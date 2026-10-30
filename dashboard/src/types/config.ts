/**
 * 配置相关类型定义
 * 与 /config 返回结构保持一致（内部使用驼峰形式）
 */

export interface Config {
  debug?: boolean;
  requestRetry?: number;
  requestLog?: boolean;
  loggingToFile?: boolean;
  wsAuth?: boolean;
  forceModelPrefix?: boolean;
  routingStrategy?: string;
  /** Client API keys, masked. */
  apiKeys?: string[];
  raw?: Record<string, unknown>;
}

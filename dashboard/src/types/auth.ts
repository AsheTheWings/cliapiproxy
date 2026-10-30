/**
 * 认证相关类型定义
 */

// 登录凭据
export interface LoginCredentials {
  apiBase: string;
  managementKey: string;
  rememberPassword?: boolean;
}

// 认证状态
export interface AuthState {
  isAuthenticated: boolean;
  apiBase: string;
  managementKey: string;
  rememberPassword: boolean;
  serverVersion: string | null;
  serverBuildDate: string | null;
}

// 连接状态
export type ConnectionStatus = 'connected' | 'disconnected' | 'connecting' | 'error';

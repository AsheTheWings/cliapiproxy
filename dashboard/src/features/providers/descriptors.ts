import type { ProviderBrand } from './types';

/** The model fields a family's groups may set. */
export interface ProviderModelCapabilities {
  maxContextLength: boolean;
  isCompat: boolean;
  configurationUpdate: boolean;
  modalities: boolean;
}

export const getProviderModelCapabilities = (brand: ProviderBrand): ProviderModelCapabilities => ({
  maxContextLength: true,
  isCompat: false,
  configurationUpdate: false,
  modalities: brand === 'openaiCompatibility',
});

/**
 * i18next holds the dashboard's English strings; English is its only language.
 */

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en.json';

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: en },
  },
  lng: 'en',
  fallbackLng: 'en',
  interpolation: {
    escapeValue: false, // React already escapes.
  },
  react: {
    useSuspense: false,
  },
});

export default i18n;

import { useTranslation } from 'react-i18next';
import { EmptyState } from '@/components/ui/EmptyState';

/** What the usage pages show while the server runs without its store. */
export function StoreUnavailable() {
  const { t } = useTranslation();
  return <EmptyState title={t('usage.store_off_title')} description={t('usage.store_off_desc')} />;
}

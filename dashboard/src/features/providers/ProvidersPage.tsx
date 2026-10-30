import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconPlus } from '@/components/ui/icons';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useConfigStore, useNotificationStore, useThemeStore } from '@/stores';
import {
  parseProviderGroups,
  providerGroupsApi,
  type ProviderFamily,
  type UpstreamGroup,
} from '@/services/api/providerGroups';
import { FAMILIES, familyDescriptor, type FamilyDescriptor } from './families';
import { GroupSheet } from './GroupSheet';
import {
  emptyDraft,
  fromDraft,
  toDraft,
  withGroup,
  withoutGroup,
  type GroupDraft,
} from './groupForm';
import styles from './ProvidersPage.module.scss';

interface SheetTarget {
  /** The group's position in its family's list, or null for a new group. */
  index: number | null;
  /** Remounts the sheet with a fresh draft each time one opens. */
  key: number;
}

const errorMessage = (error: unknown): string => {
  const data = (error as { data?: { message?: unknown; error?: unknown } })?.data;
  if (typeof data?.message === 'string') return data.message;
  if (error instanceof Error) return error.message;
  return '';
};

function FamilyIcon({ family }: { family: FamilyDescriptor }) {
  const theme = useThemeStore((state) => state.resolvedTheme);
  if ('monogram' in family.icon) {
    return (
      <span className={styles.monogram} aria-hidden="true">
        {family.icon.monogram}
      </span>
    );
  }
  return <img className={styles.familyIcon} src={family.icon[theme]} alt="" />;
}

/** Upstream API-key groups by family, which the server keeps as key state. */
export function ProvidersPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const [familyId, setFamilyId] = useState<ProviderFamily>('openai-compatibility');
  const [sheet, setSheet] = useState<SheetTarget | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  const groups = useMemo(() => parseProviderGroups(config?.raw?.['api-keys']), [config]);
  const family = familyDescriptor(familyId);
  const list = groups[familyId];

  const refresh = useCallback(async () => {
    await fetchConfig(true);
  }, [fetchConfig]);
  useHeaderRefresh(refresh);
  useEffect(() => {
    void fetchConfig().catch(() => undefined);
  }, [fetchConfig]);

  const openSheet = (index: number | null) => {
    setSheet((current) => ({ index, key: (current?.key ?? 0) + 1 }));
    setSheetOpen(true);
  };

  const replace = async (next: UpstreamGroup[], done: string) => {
    setSaving(true);
    try {
      await providerGroupsApi.replace(familyId, next);
      await fetchConfig(true);
      setSheetOpen(false);
      showNotification(done, 'success');
    } catch (error) {
      const message = errorMessage(error);
      showNotification(
        `${t('provider_groups.save_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      setSaving(false);
    }
  };

  const save = (draft: GroupDraft) => {
    if (!sheet) return;
    void replace(
      withGroup(list, sheet.index, fromDraft(draft, family)),
      t('provider_groups.saved')
    );
  };

  const remove = (index: number) => {
    showConfirmation({
      title: t('provider_groups.delete_group'),
      message: t('provider_groups.delete_confirm', { name: list[index].name }),
      variant: 'danger',
      confirmText: t('common.delete'),
      onConfirm: () => replace(withoutGroup(list, index), t('provider_groups.deleted')),
    });
  };

  const editing = sheet?.index != null ? list[sheet.index] : undefined;
  const takenNames = list.filter((_, i) => i !== sheet?.index).map((group) => group.name);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{t('nav.ai_providers')}</h1>
        <p className={styles.meta}>{t('provider_groups.subtitle')}</p>
      </header>

      <div className={styles.tabs} role="tablist" aria-label={t('provider_groups.families')}>
        {FAMILIES.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={item.id === familyId}
            className={`${styles.tab} ${item.id === familyId ? styles.tabActive : ''}`}
            onClick={() => setFamilyId(item.id)}
          >
            <FamilyIcon family={item} />
            <span>{item.label}</span>
            <span className={styles.tabCount}>{groups[item.id].length}</span>
          </button>
        ))}
      </div>

      <section className={styles.panel} role="tabpanel">
        <div className={styles.toolbar}>
          <p className={styles.familyHint}>
            {t(`provider_groups.hint_${familyId.replace('-', '_')}`)}
          </p>
          <Button onClick={() => openSheet(null)} disabled={saving}>
            <IconPlus size={14} /> {t('provider_groups.add_group')}
          </Button>
        </div>

        {list.length === 0 ? (
          <EmptyState
            title={t('provider_groups.empty_title', { family: family.label })}
            description={t('provider_groups.empty_desc')}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('provider_groups.field_name')}</TableHead>
                {family.baseUrl !== 'none' && (
                  <TableHead>{t('provider_groups.field_base_url')}</TableHead>
                )}
                <TableHead>{t('provider_groups.field_prefix')}</TableHead>
                <TableHead alignRight>{t('provider_groups.models_title')}</TableHead>
                <TableHead alignRight>{t('provider_groups.keys_title')}</TableHead>
                <TableHead alignRight>{t('common.action')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((group, index) => (
                <TableRow key={group.name}>
                  <TableCell>
                    <span className={styles.groupName}>{group.name}</span>
                  </TableCell>
                  {family.baseUrl !== 'none' && (
                    <TableCell>
                      <code className={styles.mono}>{group['base-url'] ?? '—'}</code>
                    </TableCell>
                  )}
                  <TableCell>
                    <code className={styles.mono}>{group.prefix ?? '—'}</code>
                  </TableCell>
                  <TableCell alignRight>
                    {group.models?.length
                      ? group.models.length
                      : t('provider_groups.models_catalog')}
                  </TableCell>
                  <TableCell alignRight>{group.keys.length}</TableCell>
                  <TableCell alignRight>
                    <div className={styles.rowActions}>
                      <Button variant="ghost" size="sm" onClick={() => openSheet(index)}>
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={saving}
                        onClick={() => remove(index)}
                      >
                        {t('common.delete')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>

      {sheet && (
        <GroupSheet
          key={sheet.key}
          open={sheetOpen}
          family={family}
          editing={editing ? { name: editing.name } : null}
          initial={editing ? toDraft(editing) : emptyDraft()}
          takenNames={takenNames}
          saving={saving}
          onClose={() => setSheetOpen(false)}
          onSave={save}
          onDelete={() => sheet.index !== null && remove(sheet.index)}
        />
      )}
    </div>
  );
}

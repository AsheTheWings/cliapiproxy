import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
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
import { useMenuDismiss } from '@/hooks/useMenuDismiss';
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
  family: ProviderFamily;
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

/**
 * Upstream API-key groups by family, which the server keeps as key state. A
 * family shows a tab once it has a group; the provider menu starts the others.
 */
export function ProvidersPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const [familyId, setFamilyId] = useState<ProviderFamily | null>(null);
  const [sheet, setSheet] = useState<SheetTarget | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const closeMenu = useCallback(() => setMenuOpen(false), []);
  useMenuDismiss(menuOpen, menuRef, closeMenu);

  const groups = useMemo(() => parseProviderGroups(config?.raw?.['api-keys']), [config]);
  // A family being started keeps its tab while its first group's sheet is open.
  const shown = FAMILIES.filter(
    (item) =>
      groups[item.id].length > 0 || (sheetOpen && sheet?.index === null && sheet.family === item.id)
  );
  const active = shown.find((item) => item.id === familyId) ?? shown[0] ?? null;
  const list = active ? groups[active.id] : [];

  const refresh = useCallback(async () => {
    await fetchConfig(true);
  }, [fetchConfig]);
  useHeaderRefresh(refresh);
  useEffect(() => {
    void fetchConfig().catch(() => undefined);
  }, [fetchConfig]);

  const openSheet = (family: ProviderFamily, index: number | null) => {
    setSheet((current) => ({ family, index, key: (current?.key ?? 0) + 1 }));
    setSheetOpen(true);
  };

  const startProvider = (family: ProviderFamily) => {
    setMenuOpen(false);
    setFamilyId(family);
    openSheet(family, null);
  };

  const replace = async (family: ProviderFamily, next: UpstreamGroup[], done: string) => {
    setSaving(true);
    try {
      await providerGroupsApi.replace(family, next);
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
    const family = familyDescriptor(sheet.family);
    void replace(
      sheet.family,
      withGroup(groups[sheet.family], sheet.index, fromDraft(draft, family)),
      t('provider_groups.saved')
    );
  };

  const remove = (family: ProviderFamily, index: number) => {
    const target = groups[family];
    showConfirmation({
      title: t('provider_groups.delete_group'),
      message: t('provider_groups.delete_confirm', { name: target[index].name }),
      variant: 'danger',
      confirmText: t('common.delete'),
      onConfirm: () => replace(family, withoutGroup(target, index), t('provider_groups.deleted')),
    });
  };

  const sheetFamily = sheet ? familyDescriptor(sheet.family) : null;
  const sheetList = sheet ? groups[sheet.family] : [];
  const editing = sheet?.index != null ? sheetList[sheet.index] : undefined;
  const takenNames = sheetList.filter((_, i) => i !== sheet?.index).map((group) => group.name);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{t('nav.ai_providers')}</h1>
        <p className={styles.meta}>{t('provider_groups.subtitle')}</p>
        <p className={styles.meta}>
          {t('provider_groups.oauth_hint')}{' '}
          <Link className={styles.link} to="/oauth">
            {t('nav.oauth')}
          </Link>
        </p>
      </header>

      <div className={styles.tabBar}>
        <div className={styles.tabs} role="tablist" aria-label={t('provider_groups.families')}>
          {shown.map((item) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={item.id === active?.id}
              className={`${styles.tab} ${item.id === active?.id ? styles.tabActive : ''}`}
              onClick={() => setFamilyId(item.id)}
            >
              <FamilyIcon family={item} />
              <span>{item.label}</span>
              <span className={styles.tabCount}>{groups[item.id].length}</span>
            </button>
          ))}
        </div>
        <div className={styles.providerMenu} ref={menuRef}>
          <Button
            variant="secondary"
            onClick={() => setMenuOpen((open) => !open)}
            disabled={saving}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
          >
            <IconPlus size={14} /> {t('provider_groups.add_provider')}
          </Button>
          {menuOpen && (
            <div className={styles.providerMenuPopover} role="menu">
              {FAMILIES.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  role="menuitem"
                  className={styles.providerMenuItem}
                  onClick={() => startProvider(item.id)}
                >
                  <FamilyIcon family={item} />
                  <span className={styles.providerMenuText}>
                    <span className={styles.providerMenuLabel}>{item.label}</span>
                    <span className={styles.providerMenuHint}>
                      {t(`provider_groups.hint_${item.id.replace('-', '_')}`)}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {!active ? (
        <EmptyState
          title={t('provider_groups.none_title')}
          description={t('provider_groups.none_desc')}
        />
      ) : (
        <section className={styles.panel} role="tabpanel">
          <div className={styles.toolbar}>
            <p className={styles.familyHint}>
              {t(`provider_groups.hint_${active.id.replace('-', '_')}`)}
            </p>
            <Button onClick={() => openSheet(active.id, null)} disabled={saving}>
              <IconPlus size={14} /> {t('provider_groups.add_group')}
            </Button>
          </div>

          {list.length === 0 ? (
            <EmptyState
              title={t('provider_groups.empty_title', { family: active.label })}
              description={t('provider_groups.empty_desc')}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('provider_groups.field_name')}</TableHead>
                  {active.baseUrl !== 'none' && (
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
                    {active.baseUrl !== 'none' && (
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
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => openSheet(active.id, index)}
                        >
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={saving}
                          onClick={() => remove(active.id, index)}
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
      )}

      {sheet && sheetFamily && (
        <GroupSheet
          key={sheet.key}
          open={sheetOpen}
          family={sheetFamily}
          editing={editing ? { name: editing.name } : null}
          initial={editing ? toDraft(editing) : emptyDraft()}
          takenNames={takenNames}
          saving={saving}
          onClose={() => setSheetOpen(false)}
          onSave={save}
          onDelete={() => sheet.index !== null && remove(sheet.family, sheet.index)}
        />
      )}
    </div>
  );
}

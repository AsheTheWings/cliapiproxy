import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Sheet } from '@/components/ui/Sheet';
import { IconCopy, IconPlus, IconRefreshCw } from '@/components/ui/icons';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { clientKeysApi, parseClientKeys, type ClientKey } from '@/services/api/clientKeys';
import { useConfigStore, useNotificationStore } from '@/stores';
import { generateSecureApiKey } from '@/utils/apiKey';
import { copyToClipboard } from '@/utils/clipboard';
import {
  MAX_NAME_LENGTH,
  clientKeyProblem,
  nameProblem,
  withClientKey,
  withoutClientKey,
} from './clientKeyForm';
import { StrengthMeter } from './StrengthMeter';
import styles from './ClientKeysPage.module.scss';

const errorMessage = (error: unknown): string => {
  const data = (error as { data?: { message?: unknown } })?.data;
  if (typeof data?.message === 'string') return data.message;
  if (error instanceof Error) return error.message;
  return '';
};

interface KeySheetProps {
  open: boolean;
  /** The key being edited, or null for a new key. */
  editing: ClientKey | null;
  saving: boolean;
  onClose: () => void;
  onSave: (item: ClientKey) => void;
}

/** Mounted per opening; a new key starts from a fresh generated key. */
function KeySheet({ open, editing, saving, onClose, onSave }: KeySheetProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [name, setName] = useState(editing?.name ?? '');
  const [key, setKey] = useState(() => editing?.key ?? generateSecureApiKey());
  const [keyError, setKeyError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);
  const trimmed = key.trim();
  // The stored key stays masked; only a key entered here can be copied or scored.
  const entered = editing === null || trimmed !== editing.key;

  const copy = async () => {
    const copied = await copyToClipboard(trimmed);
    showNotification(
      t(copied ? 'client_keys.copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };
  const submit = () => {
    const nextKeyError = clientKeyProblem(trimmed, editing?.key);
    const nextNameError = nameProblem(name.trim());
    setKeyError(nextKeyError);
    setNameError(nextNameError);
    if (!nextKeyError && !nextNameError) onSave({ key: trimmed, name: name.trim() });
  };

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="md"
      closeDisabled={saving}
      title={editing ? t('client_keys.edit_key') : t('client_keys.new_key')}
      description={t(editing ? 'client_keys.edit_hint' : 'client_keys.copy_hint')}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} loading={saving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <div className={styles.form}>
        <Input
          label={t('client_keys.name')}
          placeholder={t('client_keys.name_placeholder')}
          hint={t('client_keys.name_hint')}
          maxLength={MAX_NAME_LENGTH}
          value={name}
          disabled={saving}
          error={nameError ? t(nameError, { max: MAX_NAME_LENGTH }) : undefined}
          onChange={(event) => {
            setNameError(null);
            setName(event.target.value);
          }}
        />
        <Input
          label={t('client_keys.key')}
          className={styles.keyInput}
          autoComplete="off"
          spellCheck={false}
          placeholder={t('client_keys.key_placeholder')}
          value={key}
          disabled={saving}
          error={keyError ? t(keyError) : undefined}
          onChange={(event) => {
            setKeyError(null);
            setKey(event.target.value);
          }}
        />
        <div className={styles.keyActions}>
          <Button
            variant="secondary"
            size="sm"
            disabled={saving}
            onClick={() => {
              setKeyError(null);
              setKey(generateSecureApiKey());
            }}
          >
            <IconRefreshCw size={14} /> {t('client_keys.generate')}
          </Button>
          <Button variant="secondary" size="sm" disabled={!trimmed || !entered} onClick={copy}>
            <IconCopy size={14} /> {t('common.copy')}
          </Button>
        </div>
        <StrengthMeter value={entered ? trimmed : ''} />
      </div>
    </Sheet>
  );
}

interface SheetTarget {
  /** The key's position, or null for a new key. */
  index: number | null;
  /** Remounts the sheet with a fresh draft each time one opens. */
  key: number;
}

/** The keys clients present to use the proxy, which the server keeps as key state. */
export function ClientKeysPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const [sheet, setSheet] = useState<SheetTarget | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const keys = useMemo(() => parseClientKeys(config?.raw?.access), [config]);

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

  const replace = async (next: ClientKey[], done: string) => {
    setSaving(true);
    try {
      await clientKeysApi.replace(next);
      await fetchConfig(true);
      setSheetOpen(false);
      showNotification(done, 'success');
    } catch (error) {
      const message = errorMessage(error);
      showNotification(`${t('client_keys.save_failed')}${message ? `: ${message}` : ''}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  const label = (item: ClientKey) => (item.name ? `${item.name} (${item.key})` : item.key);

  const remove = (index: number) => {
    const last = keys.length === 1;
    showConfirmation({
      title: t('client_keys.revoke'),
      message: t(last ? 'client_keys.revoke_last_confirm' : 'client_keys.revoke_confirm', {
        key: label(keys[index]),
      }),
      variant: 'danger',
      confirmText: t('client_keys.revoke'),
      onConfirm: () => replace(withoutClientKey(keys, index), t('client_keys.revoked')),
    });
  };

  const save = (item: ClientKey) => {
    if (!sheet) return;
    void replace(
      withClientKey(keys, sheet.index, item),
      t(sheet.index === null ? 'client_keys.saved' : 'client_keys.updated')
    );
  };

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{t('nav.client_keys')}</h1>
        <p className={styles.meta}>{t('client_keys.subtitle')}</p>
      </header>

      <div className={styles.toolbar}>
        {config && keys.length === 0 ? (
          <p className={styles.refusing} role="status">
            {t('client_keys.refusing')}
          </p>
        ) : (
          <span />
        )}
        <Button onClick={() => openSheet(null)} disabled={saving}>
          <IconPlus size={14} /> {t('client_keys.add_key')}
        </Button>
      </div>

      {keys.length === 0 ? (
        <EmptyState
          title={t('client_keys.empty_title')}
          description={t('client_keys.empty_desc')}
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>#</TableHead>
              <TableHead>{t('client_keys.name')}</TableHead>
              <TableHead>{t('client_keys.key')}</TableHead>
              <TableHead alignRight>{t('common.action')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((item, index) => (
              <TableRow key={`${index}-${item.key}`}>
                <TableCell>
                  <span className={styles.index}>{index + 1}</span>
                </TableCell>
                <TableCell>
                  {item.name ? (
                    <span className={styles.name}>{item.name}</span>
                  ) : (
                    <span className={styles.unnamed}>{t('client_keys.unnamed')}</span>
                  )}
                </TableCell>
                <TableCell>
                  <code className={styles.key}>{item.key}</code>
                </TableCell>
                <TableCell alignRight>
                  <div className={styles.rowActions}>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={saving}
                      onClick={() => openSheet(index)}
                    >
                      {t('common.edit')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={saving}
                      onClick={() => remove(index)}
                    >
                      {t('client_keys.revoke')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {sheet !== null && (
        <KeySheet
          key={sheet.key}
          open={sheetOpen}
          editing={sheet.index === null ? null : (keys[sheet.index] ?? null)}
          saving={saving}
          onClose={() => setSheetOpen(false)}
          onSave={save}
        />
      )}
    </div>
  );
}

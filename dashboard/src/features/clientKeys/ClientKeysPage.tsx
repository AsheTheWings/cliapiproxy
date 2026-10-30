import { useCallback, useEffect, useMemo, useState, type CSSProperties } from 'react';
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
import { clientKeysApi } from '@/services/api/clientKeys';
import { useConfigStore, useNotificationStore } from '@/stores';
import { generateSecureApiKey } from '@/utils/apiKey';
import {
  API_KEY_STRENGTH_SEGMENTS,
  evaluateApiKeyStrength,
  type ApiKeyStrengthTier,
} from '@/utils/apiKeyStrength';
import { copyToClipboard } from '@/utils/clipboard';
import { clientKeyProblem } from './clientKeyForm';
import styles from './ClientKeysPage.module.scss';

const TIER_COLORS: Record<ApiKeyStrengthTier, string> = {
  weak: 'var(--error-color)',
  fair: 'var(--amber-color)',
  good: 'var(--success-color)',
  strong: 'var(--success-color)',
};

const errorMessage = (error: unknown): string => {
  const data = (error as { data?: { message?: unknown } })?.data;
  if (typeof data?.message === 'string') return data.message;
  if (error instanceof Error) return error.message;
  return '';
};

function StrengthMeter({ value }: { value: string }) {
  const { t } = useTranslation();
  const { tier, segments } = useMemo(() => evaluateApiKeyStrength(value), [value]);
  const label = segments === 0 ? '—' : t(`client_keys.strength_${tier}`);
  return (
    <div
      className={styles.strength}
      style={
        {
          '--strength-color': segments === 0 ? 'var(--text-quaternary)' : TIER_COLORS[tier],
        } as CSSProperties
      }
    >
      <div
        className={styles.strengthTrack}
        role="progressbar"
        aria-label={t('client_keys.strength')}
        aria-valuemin={0}
        aria-valuemax={API_KEY_STRENGTH_SEGMENTS}
        aria-valuenow={segments}
        aria-valuetext={label}
      >
        {Array.from({ length: API_KEY_STRENGTH_SEGMENTS }, (_, index) => (
          <span key={index} className={styles.strengthSegment} data-filled={index < segments} />
        ))}
      </div>
      <span className={styles.strengthLabel} aria-hidden="true">
        {label}
      </span>
    </div>
  );
}

interface AddKeySheetProps {
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (key: string) => void;
}

/** Mounted per opening, so each one starts from a fresh generated key. */
function AddKeySheet({ open, saving, onClose, onSave }: AddKeySheetProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [key, setKey] = useState(generateSecureApiKey);
  const [problem, setProblem] = useState<string | null>(null);

  const copy = async () => {
    const copied = await copyToClipboard(key.trim());
    showNotification(
      t(copied ? 'client_keys.copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };
  const submit = () => {
    const next = clientKeyProblem(key.trim());
    setProblem(next);
    if (!next) onSave(key.trim());
  };

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="md"
      closeDisabled={saving}
      title={t('client_keys.new_key')}
      description={t('client_keys.copy_hint')}
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
          label={t('client_keys.key')}
          className={styles.keyInput}
          autoComplete="off"
          spellCheck={false}
          value={key}
          disabled={saving}
          error={problem ? t(problem) : undefined}
          onChange={(event) => {
            setProblem(null);
            setKey(event.target.value);
          }}
        />
        <div className={styles.keyActions}>
          <Button
            variant="secondary"
            size="sm"
            disabled={saving}
            onClick={() => {
              setProblem(null);
              setKey(generateSecureApiKey());
            }}
          >
            <IconRefreshCw size={14} /> {t('client_keys.generate')}
          </Button>
          <Button variant="secondary" size="sm" disabled={!key.trim()} onClick={copy}>
            <IconCopy size={14} /> {t('common.copy')}
          </Button>
        </div>
        <StrengthMeter value={key.trim()} />
      </div>
    </Sheet>
  );
}

/** The keys clients present to use the proxy, which the server keeps as key state. */
export function ClientKeysPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const [sheet, setSheet] = useState<number | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const keys = useMemo(() => config?.apiKeys ?? [], [config]);

  const refresh = useCallback(async () => {
    await fetchConfig(true);
  }, [fetchConfig]);
  useHeaderRefresh(refresh);
  useEffect(() => {
    void fetchConfig().catch(() => undefined);
  }, [fetchConfig]);

  const replace = async (next: string[], done: string) => {
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

  const remove = (index: number) => {
    const last = keys.length === 1;
    showConfirmation({
      title: t('client_keys.revoke'),
      message: t(last ? 'client_keys.revoke_last_confirm' : 'client_keys.revoke_confirm', {
        key: keys[index],
      }),
      variant: 'danger',
      confirmText: t('client_keys.revoke'),
      onConfirm: () =>
        replace(
          keys.filter((_, i) => i !== index),
          t('client_keys.revoked')
        ),
    });
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
        <Button
          onClick={() => {
            setSheet((current) => (current ?? 0) + 1);
            setSheetOpen(true);
          }}
          disabled={saving}
        >
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
              <TableHead>{t('client_keys.key')}</TableHead>
              <TableHead alignRight>{t('common.action')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((key, index) => (
              <TableRow key={`${index}-${key}`}>
                <TableCell>
                  <code className={styles.key}>{key}</code>
                </TableCell>
                <TableCell alignRight>
                  <Button variant="ghost" size="sm" disabled={saving} onClick={() => remove(index)}>
                    {t('client_keys.revoke')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {sheet !== null && (
        <AddKeySheet
          key={sheet}
          open={sheetOpen}
          saving={saving}
          onClose={() => setSheetOpen(false)}
          onSave={(key) => void replace([...keys, key], t('client_keys.saved'))}
        />
      )}
    </div>
  );
}

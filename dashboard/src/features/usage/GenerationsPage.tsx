import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Select } from '@/components/ui/Select';
import { IconExternalLink, IconX } from '@/components/ui/icons';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { storeApi, type Generation, type GenerationFilter } from '@/services/api/store';
import { formatCompactNumber } from '@/utils/format';
import { formatDateTime, formatMS } from './format';
import { GenerationSheet } from './GenerationSheet';
import { UsageInsights } from './UsageInsights';
import { errorText, isStoreUnavailable } from './storeErrors';
import { StoreUnavailable } from './StoreUnavailable';
import styles from './UsagePages.module.scss';

/** A conversation is listed whole, in pages of the route's largest size. */
const CONVERSATION_PAGE_LIMIT = 500;

type TextFilter = 'provider' | 'credential' | 'model' | 'effort' | 'client' | 'conversation';
const TEXT_FILTERS: TextFilter[] = [
  'provider',
  'credential',
  'model',
  'effort',
  'client',
  'conversation',
];

interface Filters extends Record<TextFilter, string> {
  status: string;
  from: string;
  to: string;
}

const NO_FILTERS: Filters = {
  provider: '',
  credential: '',
  model: '',
  effort: '',
  client: '',
  conversation: '',
  status: '',
  from: '',
  to: '',
};

/** A datetime-local value as RFC 3339, or undefined when empty. */
const toRFC3339 = (local: string): string | undefined =>
  local ? new Date(local).toISOString() : undefined;

const toQuery = (f: Filters): GenerationFilter => ({
  ...Object.fromEntries(TEXT_FILTERS.map((k) => [k, f[k].trim()])),
  status: f.status,
  from: toRFC3339(f.from),
  to: toRFC3339(f.to),
});

function GenerationsTable({
  generations,
  onOpen,
}: {
  generations: Generation[];
  onOpen: (id: string) => void;
}) {
  const { t } = useTranslation();
  const traced = generations.some((g) => g.trace_url);
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('generations.time')}</TableHead>
          <TableHead>{t('generations.client')}</TableHead>
          <TableHead>{t('generations.provider')}</TableHead>
          <TableHead>{t('generations.account')}</TableHead>
          <TableHead>{t('generations.upstream_model')}</TableHead>
          <TableHead>{t('generations.effort')}</TableHead>
          <TableHead alignRight>{t('generations.input')}</TableHead>
          <TableHead alignRight>{t('generations.output')}</TableHead>
          <TableHead alignRight>{t('generations.usage')}</TableHead>
          <TableHead alignRight>{t('generations.duration')}</TableHead>
          {traced && <TableHead alignRight>{t('generations.trace')}</TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>
        {generations.map((g) => {
          const input =
            g.tokens.input_uncached + g.tokens.input_cache_read + g.tokens.input_cache_write;
          const output = g.tokens.output_non_reasoning + g.tokens.output_reasoning;
          return (
            <TableRow
              key={g.generation_id}
              className={styles.clickable}
              tabIndex={0}
              onClick={() => onOpen(g.generation_id)}
              onKeyDown={(e) => {
                if (e.target !== e.currentTarget) return;
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  onOpen(g.generation_id);
                }
              }}
            >
              <TableCell>
                <span className={styles.nowrap}>{formatDateTime(g.time)}</span>
                {g.status !== 'completed' && <span className={styles.tagWarn}>{g.status}</span>}
              </TableCell>
              <TableCell>
                {g.client_key_label || <code>{g.client_key_fingerprint ?? '—'}</code>}
              </TableCell>
              <TableCell>{g.provider}</TableCell>
              <TableCell>{g.account_label ?? '—'}</TableCell>
              <TableCell>
                <code>{g.upstream_model ?? g.requested_model ?? '—'}</code>
              </TableCell>
              <TableCell>{g.reasoning_effort ?? '—'}</TableCell>
              <TableCell alignRight title={String(input)}>
                {formatCompactNumber(input)}
              </TableCell>
              <TableCell alignRight title={String(output)}>
                {formatCompactNumber(output)}
              </TableCell>
              <TableCell alignRight>
                {g.usage_amount !== undefined ? `${g.usage_amount} ${g.usage_unit ?? ''}` : '—'}
              </TableCell>
              <TableCell alignRight>{formatMS(g.duration_ms)}</TableCell>
              {traced && (
                <TableCell alignRight>
                  {g.trace_url ? (
                    <a
                      className={styles.link}
                      href={g.trace_url}
                      target="_blank"
                      rel="noreferrer noopener"
                      onClick={(e) => e.stopPropagation()}
                      aria-label={t('generations.trace')}
                    >
                      <IconExternalLink size={14} />
                    </a>
                  ) : null}
                </TableCell>
              )}
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function GenerationsBrowser() {
  const { t } = useTranslation();
  const statusLabelId = useId();
  const [draft, setDraft] = useState<Filters>(NO_FILTERS);
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [generations, setGenerations] = useState<Generation[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  // Each load takes the next number; an answer from an earlier one is dropped.
  const latest = useRef(0);

  const conversation = filters.conversation.trim();

  const load = useCallback(async (f: Filters) => {
    const run = ++latest.current;
    setLoading(true);
    setError(null);
    try {
      const whole = Boolean(f.conversation.trim());
      const query = whole ? { ...toQuery(f), limit: CONVERSATION_PAGE_LIMIT } : toQuery(f);
      let page = await storeApi.generations(query);
      let all = page.generations;
      // A conversation is shown whole, in time order.
      while (whole && page.next_cursor) {
        if (run !== latest.current) return;
        page = await storeApi.generations({ ...query, cursor: page.next_cursor });
        all = [...all, ...page.generations];
      }
      if (run !== latest.current) return;
      setGenerations(whole ? [...all].reverse() : all);
      setCursor(page.next_cursor);
    } catch (e) {
      if (run === latest.current) setError(e);
    } finally {
      if (run === latest.current) setLoading(false);
    }
  }, []);

  const refresh = useCallback(() => load(filters), [load, filters]);
  useHeaderRefresh(refresh);
  useEffect(() => {
    void load(filters);
  }, [load, filters]);

  const more = async () => {
    if (!cursor) return;
    const run = ++latest.current;
    setLoading(true);
    try {
      const page = await storeApi.generations({ ...toQuery(filters), cursor });
      if (run !== latest.current) return;
      setGenerations((list) => [...(list ?? []), ...page.generations]);
      setCursor(page.next_cursor);
    } catch (e) {
      if (run === latest.current) setError(e);
    } finally {
      if (run === latest.current) setLoading(false);
    }
  };

  const apply = (next: Filters) => {
    setDraft(next);
    setFilters(next);
  };

  const openGeneration = (id: string) => {
    setOpen(id);
    setSheetOpen(true);
  };

  if (isStoreUnavailable(error)) return <StoreUnavailable />;

  return (
    <>
      <form
        className={styles.filters}
        onSubmit={(e) => {
          e.preventDefault();
          apply(draft);
        }}
      >
        {TEXT_FILTERS.map((k) => (
          <Input
            key={k}
            label={t(`generations.filter_${k}`)}
            value={draft[k]}
            spellCheck={false}
            onChange={(e) => setDraft({ ...draft, [k]: e.target.value })}
          />
        ))}
        <div className="form-group">
          <label id={statusLabelId}>{t('generations.filter_status')}</label>
          <Select
            value={draft.status}
            onChange={(status) => setDraft({ ...draft, status })}
            ariaLabelledBy={statusLabelId}
            fullWidth
            options={[
              { value: '', label: t('generations.any_status') },
              { value: 'completed', label: 'completed' },
              { value: 'incomplete', label: 'incomplete' },
            ]}
          />
        </div>
        <Input
          label={t('generations.filter_from')}
          type="datetime-local"
          value={draft.from}
          onChange={(e) => setDraft({ ...draft, from: e.target.value })}
        />
        <Input
          label={t('generations.filter_to')}
          type="datetime-local"
          value={draft.to}
          onChange={(e) => setDraft({ ...draft, to: e.target.value })}
        />
        <div className={styles.filterActions}>
          <Button type="submit" loading={loading}>
            {t('generations.apply')}
          </Button>
          <Button type="button" variant="secondary" onClick={() => apply(NO_FILTERS)}>
            {t('generations.clear')}
          </Button>
        </div>
      </form>

      {conversation && (
        <div className={styles.banner} role="status">
          <span>{t('generations.showing_conversation', { id: conversation })}</span>
          <Button variant="ghost" size="sm" onClick={() => apply({ ...filters, conversation: '' })}>
            <IconX size={14} /> {t('generations.leave_conversation')}
          </Button>
        </div>
      )}

      {error ? (
        <p className={styles.error}>{errorText(error)}</p>
      ) : generations === null ? (
        <LoadingSpinner />
      ) : generations.length === 0 ? (
        <EmptyState
          title={t('generations.empty_title')}
          description={t('generations.empty_desc')}
        />
      ) : (
        <>
          <GenerationsTable generations={generations} onOpen={openGeneration} />
          {cursor && !conversation && (
            <div className={styles.more}>
              <Button variant="secondary" loading={loading} onClick={() => void more()}>
                {t('generations.load_more')}
              </Button>
            </div>
          )}
        </>
      )}

      <GenerationSheet
        id={open}
        open={sheetOpen}
        onClose={() => setSheetOpen(false)}
        onConversation={(id) => {
          setSheetOpen(false);
          apply({ ...NO_FILTERS, conversation: id });
        }}
      />
    </>
  );
}

type Tab = 'browse' | 'insights';

/** Stored generations: a filterable list with payloads, and usage insights. */
export function GenerationsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>('browse');
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{t('nav.generations')}</h1>
        <p className={styles.meta}>{t('generations.subtitle')}</p>
      </header>
      <div className={styles.tabs} role="tablist">
        {(['browse', 'insights'] as Tab[]).map((k) => (
          <button
            key={k}
            type="button"
            role="tab"
            aria-selected={tab === k}
            className={tab === k ? styles.tabActive : styles.tab}
            onClick={() => setTab(k)}
          >
            {t(`generations.tab_${k}`)}
          </button>
        ))}
      </div>
      {tab === 'browse' ? <GenerationsBrowser /> : <UsageInsights />}
    </div>
  );
}

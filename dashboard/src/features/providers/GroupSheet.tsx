import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Sheet } from '@/components/ui/Sheet';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { IconPlus, IconTrash2 } from '@/components/ui/icons';
import { THINKING_LEVELS } from './thinkingLevels';
import type { FamilyDescriptor } from './families';
import {
  emptyModel,
  validateDraft,
  type DraftError,
  type GroupDraft,
  type KeyDraft,
  type ModelDraft,
} from './groupForm';
import styles from './ProvidersPage.module.scss';

export interface GroupSheetProps {
  open: boolean;
  family: FamilyDescriptor;
  /** The group being edited, or null for a new group. */
  editing: { name: string } | null;
  initial: GroupDraft;
  takenNames: string[];
  saving: boolean;
  onClose: () => void;
  onSave: (draft: GroupDraft) => void;
  onDelete: () => void;
}

/** Mounted per opened group, so its draft starts from `initial`. */
export function GroupSheet({
  open,
  family,
  editing,
  initial,
  takenNames,
  saving,
  onClose,
  onSave,
  onDelete,
}: GroupSheetProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<GroupDraft>(initial);
  const [error, setError] = useState<DraftError | null>(null);
  const disabled = saving;

  const update = (patch: Partial<GroupDraft>) => {
    setError(null);
    setDraft((current) => ({ ...current, ...patch }));
  };
  const updateModel = (index: number, patch: Partial<ModelDraft>) =>
    update({ models: draft.models.map((m, i) => (i === index ? { ...m, ...patch } : m)) });
  const updateKey = (index: number, patch: Partial<KeyDraft>) =>
    update({ keys: draft.keys.map((k, i) => (i === index ? { ...k, ...patch } : k)) });

  const submit = () => {
    const problem = validateDraft(draft, family, takenNames);
    setError(problem);
    if (!problem) onSave(draft);
  };

  const toggleLevel = (index: number, level: string) => {
    const levels = draft.models[index].thinkingLevels;
    updateModel(index, {
      thinkingLevels: levels.includes(level)
        ? levels.filter((item) => item !== level)
        : [...levels, level],
    });
  };

  const footer = (
    <div className={styles.sheetFooter}>
      {editing ? (
        <Button variant="danger" onClick={onDelete} disabled={saving}>
          {t('provider_groups.delete_group')}
        </Button>
      ) : (
        <span />
      )}
      <div className={styles.sheetFooterActions}>
        <Button variant="secondary" onClick={onClose} disabled={saving}>
          {t('common.cancel')}
        </Button>
        <Button onClick={submit} loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </div>
  );

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="lg"
      closeDisabled={saving}
      eyebrow={family.label}
      title={editing ? editing.name : t('provider_groups.new_group')}
      footer={footer}
    >
      <div className={styles.form}>
        <Input
          label={t('provider_groups.field_name')}
          hint={t('provider_groups.field_name_hint')}
          value={draft.name}
          disabled={disabled}
          onChange={(event) => update({ name: event.target.value })}
        />
        {family.baseUrl !== 'none' && (
          <Input
            label={t(
              family.baseUrl === 'required'
                ? 'provider_groups.field_base_url_required'
                : 'provider_groups.field_base_url'
            )}
            placeholder="https://api.example.com/v1"
            value={draft.baseUrl}
            disabled={disabled}
            onChange={(event) => update({ baseUrl: event.target.value })}
          />
        )}
        <Input
          label={t('provider_groups.field_prefix')}
          hint={t('provider_groups.field_prefix_hint')}
          value={draft.prefix}
          disabled={disabled}
          onChange={(event) => update({ prefix: event.target.value })}
        />

        <section className={styles.formSection}>
          <div className={styles.formSectionHeader}>
            <h3>{t('provider_groups.keys_title')}</h3>
            <Button
              variant="ghost"
              size="sm"
              disabled={saving}
              onClick={() => update({ keys: [...draft.keys, { value: '' }] })}
            >
              <IconPlus size={14} /> {t('provider_groups.add_key')}
            </Button>
          </div>
          {draft.keys.map((key, index) => (
            <div className={styles.listRow} key={index}>
              {key.stored ? (
                <code className={styles.storedKey}>{key.stored}</code>
              ) : (
                <input
                  className="input"
                  type="password"
                  autoComplete="off"
                  placeholder={t('provider_groups.key_placeholder')}
                  aria-label={t('provider_groups.key_placeholder')}
                  value={key.value}
                  disabled={disabled}
                  onChange={(event) => updateKey(index, { value: event.target.value })}
                />
              )}
              <Button
                variant="ghost"
                size="sm"
                disabled={saving}
                aria-label={t('provider_groups.remove_key')}
                onClick={() => update({ keys: draft.keys.filter((_, i) => i !== index) })}
              >
                <IconTrash2 size={14} />
              </Button>
            </div>
          ))}
        </section>

        <section className={styles.formSection}>
          <div className={styles.formSectionHeader}>
            <h3>{t('provider_groups.models_title')}</h3>
            <Button
              variant="ghost"
              size="sm"
              disabled={saving}
              onClick={() => update({ models: [...draft.models, emptyModel()] })}
            >
              <IconPlus size={14} /> {t('provider_groups.add_model')}
            </Button>
          </div>
          <p className={styles.sectionHint}>
            {t(
              family.modelsRequired
                ? 'provider_groups.models_hint_required'
                : 'provider_groups.models_hint_catalog'
            )}
          </p>
          {draft.models.map((model, index) => (
            <fieldset className={styles.modelCard} key={index} disabled={disabled}>
              <div className={styles.modelGrid}>
                <Input
                  label={t('provider_groups.model_name')}
                  value={model.name}
                  onChange={(event) => updateModel(index, { name: event.target.value })}
                />
                <Input
                  label={t('provider_groups.model_alias')}
                  value={model.alias}
                  onChange={(event) => updateModel(index, { alias: event.target.value })}
                />
                <Input
                  label={t('provider_groups.model_display_name')}
                  value={model.displayName}
                  onChange={(event) => updateModel(index, { displayName: event.target.value })}
                />
                <Input
                  label={t('provider_groups.model_context')}
                  inputMode="numeric"
                  value={model.maxContextLength}
                  onChange={(event) => updateModel(index, { maxContextLength: event.target.value })}
                />
                {family.inputModalities && (
                  <Input
                    label={t('provider_groups.model_modalities')}
                    placeholder="text, image"
                    value={model.inputModalities}
                    onChange={(event) =>
                      updateModel(index, { inputModalities: event.target.value })
                    }
                  />
                )}
              </div>
              <div
                className={styles.levels}
                role="group"
                aria-label={t('provider_groups.model_levels')}
              >
                <span className={styles.levelsLabel}>{t('provider_groups.model_levels')}</span>
                {[
                  ...THINKING_LEVELS,
                  ...model.thinkingLevels.filter(
                    (level) => !(THINKING_LEVELS as readonly string[]).includes(level)
                  ),
                ].map((level) => (
                  <label className={styles.level} key={level}>
                    <input
                      type="checkbox"
                      checked={model.thinkingLevels.includes(level)}
                      onChange={() => toggleLevel(index, level)}
                    />
                    <code>{level}</code>
                  </label>
                ))}
              </div>
              <div className={styles.modelActions}>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => update({ models: draft.models.filter((_, i) => i !== index) })}
                >
                  <IconTrash2 size={14} /> {t('provider_groups.remove_model')}
                </Button>
              </div>
            </fieldset>
          ))}
        </section>

        {error && (
          <div className="error-box" role="alert">
            {t(error.key, error.values)}
          </div>
        )}
      </div>
    </Sheet>
  );
}

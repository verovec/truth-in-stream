"use client";

import { type FormEvent, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import {
  type Channel,
  type ChannelCreateInput,
  type ChannelUpdateInput,
  SLUG_PATTERN,
  type SourceKind,
  SOURCE_KINDS,
} from "@/lib/tv/api";

// FieldErrors keeps client-side validation failures per field so a shown error
// re-labels itself when the admin switches locale.
type FieldErrors = Partial<
  Record<"slug" | "name" | "sourceKind" | "sourceRef", true>
>;

// ServerError keeps a rejected save as data (message or null) so it re-labels on
// a locale switch and distinguishes a backend message from the generic fallback.
type ServerError = { message: string | null };

const INPUT_CLASS =
  "w-full rounded-md border border-black/15 bg-white px-3 py-1.5 text-sm text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper";
const ERROR_CLASS = "text-xs text-rouge dark:text-rose-300";

// BackofficeTvChannelForm is the add/edit surface for a channel. It validates the
// slug (kebab), name, source kind, and source ref in the browser before any
// request leaves, then surfaces the backend's own message inline for a 400
// (invalid) or 409 (duplicate slug). In edit mode the slug is immutable (the
// backend PATCH does not accept it), so it renders read-only and is never sent.
// State is seeded from `editing` via the initial useState values; the parent
// remounts the form with a key when the edit target changes, so there is no
// derive-from-props effect. create and update are the only side effects; the
// parent owns the catalog and re-lists it via onSaved.
export function BackofficeTvChannelForm({
  editing,
  create,
  update,
  onSaved,
  onCancelEdit,
}: {
  editing: Channel | null;
  create: (input: ChannelCreateInput, signal?: AbortSignal) => Promise<Channel>;
  update: (
    id: string,
    patch: ChannelUpdateInput,
    signal?: AbortSignal,
  ) => Promise<Channel>;
  onSaved: () => void;
  onCancelEdit: () => void;
}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.tv.form;
  const isEdit = editing !== null;

  const [slug, setSlug] = useState(editing?.slug ?? "");
  const [name, setName] = useState(editing?.name ?? "");
  const [sourceKind, setSourceKind] = useState<SourceKind>(
    editing?.sourceKind ?? "youtube",
  );
  const [sourceRef, setSourceRef] = useState(editing?.sourceRef ?? "");
  const [enabled, setEnabled] = useState(editing?.enabled ?? true);
  const [archiveEnabled, setArchiveEnabled] = useState(
    editing?.archiveEnabled ?? false,
  );
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [serverError, setServerError] = useState<ServerError | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const validate = (): FieldErrors => {
    const errors: FieldErrors = {};
    if (!isEdit && !SLUG_PATTERN.test(slug.trim())) {
      errors.slug = true;
    }
    if (name.trim() === "") {
      errors.name = true;
    }
    if (!(SOURCE_KINDS as readonly string[]).includes(sourceKind)) {
      errors.sourceKind = true;
    }
    if (sourceRef.trim() === "") {
      errors.sourceRef = true;
    }
    return errors;
  };

  const resetAddFields = () => {
    setSlug("");
    setName("");
    setSourceKind("youtube");
    setSourceRef("");
    setEnabled(true);
    setArchiveEnabled(false);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setServerError(null);
    const errors = validate();
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) {
      return;
    }
    setSubmitting(true);
    try {
      if (editing) {
        await update(editing.id, {
          name: name.trim(),
          sourceKind,
          sourceRef: sourceRef.trim(),
          enabled,
          archiveEnabled,
        });
      } else {
        await create({
          slug: slug.trim(),
          name: name.trim(),
          sourceKind,
          sourceRef: sourceRef.trim(),
          enabled,
          archiveEnabled,
        });
        resetAddFields();
      }
      onSaved();
    } catch (err) {
      setServerError({ message: err instanceof Error ? err.message : null });
    } finally {
      setSubmitting(false);
    }
  };

  const heading = isEdit
    ? formatTemplate(copy.editHeading, { name: editing.name })
    : copy.addHeading;

  return (
    <form
      onSubmit={submit}
      aria-label={heading}
      className="flex flex-col gap-3 rounded-xl border border-black/10 bg-white p-4 dark:border-white/10 dark:bg-white/5"
    >
      <h4 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
        {heading}
      </h4>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-ink/80 dark:text-paper/80">
            {copy.slug}
          </span>
          <input
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            disabled={isEdit}
            aria-invalid={fieldErrors.slug ? true : undefined}
            className={INPUT_CLASS + (isEdit ? " opacity-60" : "")}
          />
          {fieldErrors.slug ? (
            <span role="alert" className={ERROR_CLASS}>
              {copy.errors.slug}
            </span>
          ) : null}
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-ink/80 dark:text-paper/80">
            {copy.name}
          </span>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={fieldErrors.name ? true : undefined}
            className={INPUT_CLASS}
          />
          {fieldErrors.name ? (
            <span role="alert" className={ERROR_CLASS}>
              {copy.errors.name}
            </span>
          ) : null}
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-ink/80 dark:text-paper/80">
            {copy.sourceKind}
          </span>
          <select
            value={sourceKind}
            onChange={(e) => setSourceKind(e.target.value as SourceKind)}
            aria-invalid={fieldErrors.sourceKind ? true : undefined}
            className={INPUT_CLASS}
          >
            <option value="youtube">{copy.kinds.youtube}</option>
            <option value="hls">{copy.kinds.hls}</option>
          </select>
          {fieldErrors.sourceKind ? (
            <span role="alert" className={ERROR_CLASS}>
              {copy.errors.sourceKind}
            </span>
          ) : null}
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-ink/80 dark:text-paper/80">
            {copy.sourceRef}
          </span>
          <input
            type="text"
            value={sourceRef}
            onChange={(e) => setSourceRef(e.target.value)}
            aria-invalid={fieldErrors.sourceRef ? true : undefined}
            className={INPUT_CLASS}
          />
          {fieldErrors.sourceRef ? (
            <span role="alert" className={ERROR_CLASS}>
              {copy.errors.sourceRef}
            </span>
          ) : null}
        </label>
      </div>
      <div className="flex flex-wrap gap-4">
        <label className="flex items-center gap-2 text-sm text-ink/80 dark:text-paper/80">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          {copy.enabled}
        </label>
        <label className="flex items-center gap-2 text-sm text-ink/80 dark:text-paper/80">
          <input
            type="checkbox"
            checked={archiveEnabled}
            onChange={(e) => setArchiveEnabled(e.target.checked)}
          />
          {copy.archive}
        </label>
      </div>
      {serverError ? (
        <p role="alert" className={ERROR_CLASS}>
          {serverError.message === null
            ? copy.saveErrorFallback
            : formatTemplate(copy.saveError, { message: serverError.message })}
        </p>
      ) : null}
      <div className="flex items-center gap-2">
        <button
          type="submit"
          disabled={submitting}
          className="rounded-md border border-bleu-flag/30 bg-bleu-flag/10 px-3 py-1.5 text-sm font-medium text-bleu-flag hover:bg-bleu-flag/15 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-sky-300"
        >
          {submitting ? copy.saving : isEdit ? copy.submitEdit : copy.submitAdd}
        </button>
        {isEdit ? (
          <button
            type="button"
            onClick={onCancelEdit}
            disabled={submitting}
            className="rounded-md px-3 py-1.5 text-sm font-medium text-ink/60 hover:bg-black/5 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {copy.cancel}
          </button>
        ) : null}
      </div>
    </form>
  );
}

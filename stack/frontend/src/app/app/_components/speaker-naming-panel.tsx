"use client";

import type { FormEvent } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { SpeakerNaming } from "@/hooks/use-video-speakers";
import type { Role } from "@/lib/auth/token";
import { formatTemplate } from "@/lib/i18n/text";
import { formatTime } from "@/lib/playback/format-time";
import {
  SPEAKER_ROLES,
  type SpeakerEvidence,
  type SpeakerRole,
  type VideoSpeaker,
} from "@/lib/video/speakers";

const FIELD_CLASS =
  "rounded-md border border-black/10 bg-white px-2 py-1 text-xs text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper dark:focus-visible:outline-paper/60";

const BUTTON_CLASS =
  "rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60";

function isSpeakerRole(value: FormDataEntryValue | null): value is SpeakerRole {
  return SPEAKER_ROLES.some((role) => role === value);
}

// SpeakerNamingPanel shows who the diarized voices of an analysed video are:
// one row per voice with its name (or that it is unnamed), role, how sure the
// naming step was, and the transcript evidence behind the name. An admin can
// also correct a name, set a role and confirm it, or re-run the step. The
// admin gate is presentation only: the role comes from the server-verified
// session and the backend enforces it regardless. A guest sees the panel only
// when there is a mapping to read.
export function SpeakerNamingPanel({
  role,
  naming,
}: {
  role: Role;
  naming: SpeakerNaming;
}) {
  const { t } = useAppI18n();
  const copy = t.speakerNaming;
  const admin = role === "admin";
  if (!admin && naming.speakers.length === 0) {
    return null;
  }
  return (
    <section
      aria-label={copy.heading}
      className="flex w-full flex-col gap-3 rounded-2xl border border-black/10 bg-white px-4 py-3 dark:border-white/10 dark:bg-white/5"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.heading}
        </h2>
        {admin ? (
          <button
            type="button"
            onClick={naming.rerun}
            disabled={naming.rerunning}
            className={BUTTON_CLASS}
          >
            {naming.rerunning ? copy.rerunning : copy.rerun}
          </button>
        ) : null}
      </div>
      {naming.error ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {copy.errors[naming.error]}
        </p>
      ) : null}
      {naming.speakers.length === 0 ? (
        <p className="text-xs text-ink/60 dark:text-paper/60">{copy.empty}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-black/5 dark:divide-white/10">
          {naming.speakers.map((speaker) => (
            <SpeakerNamingRow
              key={speaker.label}
              speaker={speaker}
              admin={admin}
              saving={naming.savingLabels.has(speaker.label)}
              onSave={naming.save}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function SpeakerNamingRow({
  speaker,
  admin,
  saving,
  onSave,
}: {
  speaker: VideoSpeaker;
  admin: boolean;
  saving: boolean;
  onSave: SpeakerNaming["save"];
}) {
  const { t } = useAppI18n();
  const copy = t.speakerNaming;
  const voice = formatTemplate(copy.voice, { label: speaker.label });
  return (
    <li className="flex flex-col gap-1.5 py-2 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="text-[11px] font-semibold uppercase tracking-wide text-ink/50 dark:text-paper/50">
          {voice}
        </span>
        <span className="text-sm font-semibold text-ink dark:text-paper">
          {speaker.name === "" ? copy.unnamed : speaker.name}
        </span>
        <span className="text-xs text-ink/60 dark:text-paper/60">
          {copy.roles[speaker.role]}
        </span>
        <span className="text-xs text-ink/60 dark:text-paper/60">
          <SpeakerCertainty speaker={speaker} />
        </span>
      </div>
      <SpeakerEvidenceList evidence={speaker.evidence} />
      {admin ? (
        // Keyed on the stored decision so the uncontrolled fields pick up a
        // saved or re-run value instead of keeping a stale draft.
        <SpeakerForm
          key={`${speaker.name}|${speaker.role}`}
          speaker={speaker}
          saving={saving}
          onSave={onSave}
        />
      ) : null}
    </li>
  );
}

// SpeakerCertainty states where the name comes from: an operator's decision,
// or the naming step's confidence. An unnamed voice carries neither.
function SpeakerCertainty({ speaker }: { speaker: VideoSpeaker }) {
  const { t } = useAppI18n();
  const copy = t.speakerNaming;
  if (speaker.operatorSet) {
    return copy.confirmed;
  }
  if (speaker.name === "") {
    return null;
  }
  return formatTemplate(copy.confidence, {
    pct: Math.round(speaker.confidence * 100),
  });
}

function SpeakerEvidenceList({ evidence }: { evidence: SpeakerEvidence[] }) {
  const { t } = useAppI18n();
  const copy = t.speakerNaming.evidence;
  if (evidence.length === 0) {
    return null;
  }
  return (
    <details className="text-xs text-ink/70 dark:text-paper/70">
      <summary className="cursor-pointer select-none text-ink/60 dark:text-paper/60">
        {formatTemplate(copy.summary, { count: evidence.length })}
      </summary>
      <ul className="mt-1 flex flex-col gap-1 pl-3">
        {evidence.map((item, index) => (
          <li key={`${item.at}-${index}`} className="flex flex-wrap gap-x-2">
            <span className="tabular-nums text-ink/50 dark:text-paper/50">
              {formatTime(item.at)}
            </span>
            <span className="text-ink/50 dark:text-paper/50">
              {copy.kinds[item.kind as keyof typeof copy.kinds] ?? item.kind}
            </span>
            <q>{item.quote}</q>
          </li>
        ))}
      </ul>
    </details>
  );
}

function SpeakerForm({
  speaker,
  saving,
  onSave,
}: {
  speaker: VideoSpeaker;
  saving: boolean;
  onSave: SpeakerNaming["save"];
}) {
  const { t } = useAppI18n();
  const copy = t.speakerNaming;
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const name = data.get("name");
    const role = data.get("role");
    onSave(speaker.label, {
      name: typeof name === "string" ? name.trim() : "",
      role: isSpeakerRole(role) ? role : speaker.role,
    });
  };
  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
      <input
        name="name"
        type="text"
        defaultValue={speaker.name}
        maxLength={120}
        aria-label={formatTemplate(copy.form.name, { label: speaker.label })}
        placeholder={copy.form.namePlaceholder}
        className={`${FIELD_CLASS} min-w-0 flex-1`}
      />
      <select
        name="role"
        defaultValue={speaker.role}
        aria-label={formatTemplate(copy.form.role, { label: speaker.label })}
        className={FIELD_CLASS}
      >
        {SPEAKER_ROLES.map((role) => (
          <option key={role} value={role}>
            {copy.roles[role]}
          </option>
        ))}
      </select>
      <button type="submit" disabled={saving} className={BUTTON_CLASS}>
        {saving ? copy.form.saving : copy.form.confirm}
      </button>
    </form>
  );
}

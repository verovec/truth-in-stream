// Typed client and pure helpers for the recap API (stack/backend
// internal/handler/video_recap.go): an analysed video presented as a report.
// The backend projection is the single source of every figure; nothing here
// recomputes a count from frames.
import { API_BASE, toApiError } from "@/lib/http";
import { SPEAKER_ROLES, type SpeakerRole } from "@/lib/video/speakers";

// RecapCounts tallies statements by outcome. The four outcomes are exclusive
// and detected is their sum: notVerified is a statement the pipeline could not
// judge, unverifiable one it judged and found unsupported either way.
export type RecapCounts = {
  detected: number;
  credible: number;
  disputed: number;
  unverifiable: number;
  notVerified: number;
};

export type RecapSource = { title: string; url: string };

// RecapStatement is one judged statement. quote is verbatim transcript text;
// at is its video time in seconds.
export type RecapStatement = {
  claimId: string;
  at: number;
  verdict: string;
  flags: string[];
  quote: string;
  rationale: string;
  sources: RecapSource[];
};

// RecapSpeaker is one diarized voice. name is empty for an unnamed voice,
// which the report prints under its label; share is its fraction of the
// principals' speaking time (zero for a non-principal).
export type RecapSpeaker = {
  label: string;
  name: string;
  role: SpeakerRole;
  nameConfirmed: boolean;
  speakingTime: number;
  turns: number;
  share: number;
  counts: RecapCounts;
  disputed: RecapStatement[];
  credible: RecapStatement[];
};

export type RecapGroup = {
  speakingTime: number;
  turns: number;
  counts: RecapCounts;
};

// Recap is the whole report. principals sums the voices that get a sheet,
// others the rest; together they are the whole analysis.
export type Recap = {
  videoId: string;
  title: string;
  analyzedAt: string | null;
  duration: number;
  speakers: RecapSpeaker[];
  principals: RecapGroup;
  others: RecapGroup;
};

const ZERO_COUNTS: RecapCounts = {
  detected: 0,
  credible: 0,
  disputed: 0,
  unverifiable: 0,
  notVerified: 0,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function num(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function parseCounts(value: unknown): RecapCounts {
  if (!isRecord(value)) {
    return ZERO_COUNTS;
  }
  return {
    detected: num(value.detected),
    credible: num(value.credible),
    disputed: num(value.disputed),
    unverifiable: num(value.unverifiable),
    notVerified: num(value.not_verified),
  };
}

// isHttpUrl keeps only links a browser may follow from a report: a source is
// model- and corpus-supplied text, so a javascript: or data: URL must never
// reach an href.
function isHttpUrl(value: string): boolean {
  try {
    const { protocol } = new URL(value);
    return protocol === "http:" || protocol === "https:";
  } catch {
    return false;
  }
}

function parseSources(value: unknown): RecapSource[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const sources: RecapSource[] = [];
  for (const item of value) {
    if (!isRecord(item) || !isHttpUrl(str(item.url))) {
      continue;
    }
    const url = str(item.url);
    if (!sources.some((source) => source.url === url)) {
      sources.push({ title: str(item.title) || url, url });
    }
  }
  return sources;
}

// parseFlags keeps the distinct string flags, in order.
function parseFlags(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return [
    ...new Set(value.filter((flag): flag is string => typeof flag === "string")),
  ];
}

// parseDate keeps a timestamp only when it is one, so a malformed value reads
// as an unknown date instead of failing the page when it is formatted.
function parseDate(value: unknown): string | null {
  return typeof value === "string" && !Number.isNaN(Date.parse(value))
    ? value
    : null;
}

function parseStatements(value: unknown): RecapStatement[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) =>
    isRecord(item)
      ? [
          {
            claimId: str(item.claim_id),
            at: num(item.at),
            verdict: str(item.verdict),
            flags: parseFlags(item.flags),
            quote: str(item.quote),
            rationale: str(item.rationale),
            sources: parseSources(item.sources),
          },
        ]
      : [],
  );
}

function parseGroup(value: unknown): RecapGroup {
  const group = isRecord(value) ? value : {};
  return {
    speakingTime: num(group.speaking_time),
    turns: num(group.turns),
    counts: parseCounts(group.counts),
  };
}

function parseSpeakers(value: unknown): RecapSpeaker[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) => {
    if (!isRecord(item) || typeof item.label !== "string") {
      return [];
    }
    const role = SPEAKER_ROLES.find((candidate) => candidate === item.role);
    return [
      {
        label: item.label,
        name: str(item.name),
        role: role ?? "unknown",
        nameConfirmed: item.name_confirmed === true,
        speakingTime: num(item.speaking_time),
        turns: num(item.turns),
        share: num(item.share),
        counts: parseCounts(item.counts),
        disputed: parseStatements(item.disputed),
        credible: parseStatements(item.credible),
      },
    ];
  });
}

// parseRecap validates the wire recap, degrading each malformed part to its
// neutral value so one bad field never costs the report.
export function parseRecap(value: unknown): Recap {
  const wire = isRecord(value) ? value : {};
  return {
    videoId: str(wire.video_id),
    title: str(wire.title),
    analyzedAt: parseDate(wire.analyzed_at),
    duration: num(wire.duration),
    speakers: parseSpeakers(wire.speakers),
    principals: parseGroup(wire.principals),
    others: parseGroup(wire.others),
  };
}

// getVideoRecap fetches the recap of a video. It answers 404 for an unknown
// video and 409 while the video has no completed analysis.
export async function getVideoRecap(
  id: string,
  signal?: AbortSignal,
): Promise<Recap> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}/recap`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  return parseRecap(await response.json());
}

// addCounts sums two tallies, for the whole-analysis figures on the cover.
export function addCounts(a: RecapCounts, b: RecapCounts): RecapCounts {
  return {
    detected: a.detected + b.detected,
    credible: a.credible + b.credible,
    disputed: a.disputed + b.disputed,
    unverifiable: a.unverifiable + b.unverifiable,
    notVerified: a.notVerified + b.notVerified,
  };
}

// unverifiedShare is the fraction of detected statements that carry no
// verdict a reader can rely on: unverifiable plus not verified. Zero when
// nothing was detected.
export function unverifiedShare(counts: RecapCounts): number {
  return counts.detected === 0
    ? 0
    : (counts.unverifiable + counts.notVerified) / counts.detected;
}

// ORDER_PARAM is the query parameter that carries the sheet order, so a
// reload, a shared link and the PDF export all see the same arrangement.
export const ORDER_PARAM = "order";

// ORDER_SEPARATOR joins sheet labels in the URL's order parameter. Diarized
// labels are short letter codes, so a comma never collides with one.
const ORDER_SEPARATOR = ",";

export function parseSheetOrder(param: string | null | undefined): string[] {
  return param ? param.split(ORDER_SEPARATOR).filter((label) => label !== "") : [];
}

export function serializeSheetOrder(labels: string[]): string {
  return labels.join(ORDER_SEPARATOR);
}

// orderSheets arranges the principals for the sheets. With no requested order
// they keep the recap's own order, speaking time descending. A requested order
// is applied as far as it names existing principals; principals it omits (a
// stale link, a voice re-classified since) follow in the default order, so an
// outdated URL never hides a sheet.
export function orderSheets(
  speakers: RecapSpeaker[],
  requested: string[],
): RecapSpeaker[] {
  const principals = speakers.filter((speaker) => speaker.role === "principal");
  const byLabel = new Map(principals.map((speaker) => [speaker.label, speaker]));
  const ordered: RecapSpeaker[] = [];
  for (const label of requested) {
    const speaker = byLabel.get(label);
    if (speaker && !ordered.includes(speaker)) {
      ordered.push(speaker);
    }
  }
  return [...ordered, ...principals.filter((speaker) => !ordered.includes(speaker))];
}

// moveSheet returns labels with the one at index moved by offset, or the same
// list when the move would leave its bounds.
export function moveSheet(labels: string[], index: number, offset: number): string[] {
  const target = index + offset;
  if (index < 0 || index >= labels.length || target < 0 || target >= labels.length) {
    return labels;
  }
  const next = [...labels];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}

// QUOTE_LIMIT bounds a printed quote, on the page and in the PDF alike.
export const QUOTE_LIMIT = 280;

// truncateQuote shortens a verbatim quote to at most limit characters, cutting
// on a word boundary and marking the cut with an ellipsis. The words kept are
// never altered.
export function truncateQuote(quote: string, limit: number): string {
  const text = quote.trim();
  if (text.length <= limit) {
    return text;
  }
  const head = text.slice(0, limit);
  const cut = head.lastIndexOf(" ");
  return `${(cut > 0 ? head.slice(0, cut) : head).replace(/[\s,;:]+$/, "")}…`;
}

// REPORT_TIME_ZONE is the zone the report's dates are printed in. It is fixed,
// not the viewer's, so a page and the PDF made from it always carry the same
// date, and it is the product's audience's zone rather than UTC, which would
// print the previous day for an analysis finished after midnight in France.
export const REPORT_TIME_ZONE = "Europe/Paris";

// formatDuration renders a number of seconds as the report prints durations:
// "2 h 51", "28 min" or "45 s".
export function formatDuration(seconds: number): string {
  const total = Number.isFinite(seconds) ? Math.max(0, Math.round(seconds)) : 0;
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (hours > 0) {
    return `${hours} h ${String(minutes).padStart(2, "0")}`;
  }
  if (minutes > 0) {
    return `${minutes} min`;
  }
  return `${total} s`;
}

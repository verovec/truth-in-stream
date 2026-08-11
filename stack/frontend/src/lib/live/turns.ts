// Grouping the transcript into speaker turns for the flowing text display. The
// transcript renders one paragraph per turn - consecutive statements by the
// same speaker - with each finalised sentence flowing inline, so the display
// reads like a written text instead of stacked per-unit blocks while speaker
// separation survives. A pure projection over the merged display statements,
// shared by every driver that renders the transcript.
import type { DisplayStatement } from "./merge";
import type { Inconsistency } from "./statements";

// TurnSentence is one finalised sentence inside a turn: its own subtitle id
// (the key highlight spans anchor on) and time range, plus the id of the
// display statement it belongs to - the id selection and the fact-check list
// key on, which differs from the sentence's own id when the sentence is a
// member of a merged claim unit. The parent's inconsistency flag rides on its
// first sentence so a turn renders each statement's flag exactly once.
export type TurnSentence = {
  id: string;
  text: string;
  start: number;
  end: number;
  statementId: string;
  inconsistency?: Inconsistency;
};

// SpeakerTurn is one paragraph of the transcript: an unbroken run of sentences
// by one speaker (or by unattributed speech), spanning the run's full time
// range. id is the first sentence's id, stable for rendering keys.
export type SpeakerTurn = {
  id: string;
  speaker?: string;
  start: number;
  end: number;
  sentences: TurnSentence[];
};

// sentencesOf flattens one display statement into its sentences: a merged unit
// contributes one sentence per member part (each with its own id and time range
// so highlights and the playback clock track the original segments), a plain
// statement contributes itself. The statement's inconsistency flag attaches to
// the first sentence only.
function sentencesOf(statement: DisplayStatement): TurnSentence[] {
  const parts = statement.parts;
  if (!parts || parts.length === 0) {
    return [
      {
        id: statement.id,
        text: statement.text,
        start: statement.start,
        end: statement.end,
        statementId: statement.id,
        inconsistency: statement.inconsistency,
      },
    ];
  }
  return parts.map((part, index) => ({
    id: part.id,
    text: part.text,
    start: part.start,
    end: part.end,
    statementId: statement.id,
    inconsistency: index === 0 ? statement.inconsistency : undefined,
  }));
}

/**
 * Groups the ordered display statements into speaker turns. A turn breaks when
 * the speaker label changes; consecutive statements that both lack a speaker
 * share one unattributed turn, so an undiarized stream reads as continuous
 * paragraphs rather than one sentence per row. Input order (by start time) is
 * preserved; each turn spans from its first sentence's start to its latest
 * sentence's end.
 */
export function groupSpeakerTurns(
  statements: readonly DisplayStatement[],
): SpeakerTurn[] {
  const turns: SpeakerTurn[] = [];
  for (const statement of statements) {
    const sentences = sentencesOf(statement);
    const current = turns.at(-1);
    if (current !== undefined && current.speaker === statement.speaker) {
      current.sentences.push(...sentences);
      current.end = Math.max(current.end, statement.end);
      continue;
    }
    turns.push({
      id: sentences[0].id,
      speaker: statement.speaker,
      start: statement.start,
      end: statement.end,
      sentences,
    });
  }
  return turns;
}

// turnSentences flattens the turns back into one chronological sentence list,
// the shape active-position tracking binary-searches over.
export function turnSentences(turns: readonly SpeakerTurn[]): TurnSentence[] {
  return turns.flatMap((turn) => turn.sentences);
}

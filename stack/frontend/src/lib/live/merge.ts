// Merging a claim-bearing unit's member statements into one displayed
// statement. When the backend groups several consecutive statements into one
// analysis unit, its claims frame names the member subtitle ids (segment_ids);
// rendering those members as separate rows splits what the pipeline treated as
// one utterance and leaves the non-anchor rows without any claim outcome. The
// merge collapses the group into a single row at the position of its first
// member, keeping each member's own text slice so highlight offsets stay valid
// per original segment. It is a pure projection over the reduced state, shared
// by the live, TV, and analysed-playback drivers.
import type { LiveStatement } from "./statements";

// StatementPart is one member statement's contribution to a merged row: its
// original subtitle id (the key highlight spans anchor on), its text, and its
// own time range so the flowing transcript can track the playback position at
// sentence granularity inside a merged unit.
export type StatementPart = {
  id: string;
  text: string;
  start: number;
  end: number;
};

// DisplayStatement is what the transcript renders: a plain statement, or a
// merged unit carrying parts - the ordered member texts whose per-segment
// highlights the row renders individually. A plain statement has no parts, so
// every LiveStatement is a valid DisplayStatement.
export type DisplayStatement = LiveStatement & {
  parts?: readonly StatementPart[];
};

// mergeUnitStatements collapses each multi-member unit's statements into one
// merged row, positioned where the group's first statement was, spanning the
// group's full time range, with the anchor statement supplying every other
// field. Only units whose anchor and at least one other member are present
// merge; a unit with absent members (a superseded replay, a stale snapshot)
// merges the members it still has, and a unit reduced to its anchor - or never
// announced with members - renders per statement exactly as before. The input
// list order (by start time) is preserved.
export function mergeUnitStatements(
  statements: LiveStatement[],
  members: ReadonlyMap<string, readonly string[]>,
): DisplayStatement[] {
  if (members.size === 0) {
    return statements;
  }
  const byId = new Map(statements.map((s) => [s.id, s]));
  const memberToUnit = new Map<string, string>();
  const presentByUnit = new Map<string, LiveStatement[]>();
  for (const [unitId, ids] of members) {
    if (ids.length < 2 || !byId.has(unitId)) {
      continue;
    }
    // A duplicated id inside one unit's list contributes once; a statement a
    // previously announced unit already claimed keeps that unit's whole group
    // unmerged (first announcement wins) - either way no statement's text can
    // render twice.
    const seen = new Set<string>();
    const present = ids.flatMap((id) => {
      if (seen.has(id)) {
        return [];
      }
      seen.add(id);
      return byId.get(id) ?? [];
    });
    if (present.length < 2 || present.some((s) => memberToUnit.has(s.id))) {
      continue;
    }
    for (const s of present) {
      memberToUnit.set(s.id, unitId);
    }
    presentByUnit.set(unitId, present);
  }
  if (presentByUnit.size === 0) {
    return statements;
  }

  const merged: DisplayStatement[] = [];
  const done = new Set<string>();
  for (const statement of statements) {
    const unitId = memberToUnit.get(statement.id);
    if (unitId === undefined) {
      merged.push(statement);
      continue;
    }
    if (done.has(unitId)) {
      continue;
    }
    done.add(unitId);
    const present = presentByUnit.get(unitId) ?? [];
    const anchor = byId.get(unitId) ?? statement;
    const parts = present.map((member) => ({
      id: member.id,
      text: member.text,
      start: member.start,
      end: member.end,
    }));
    merged.push({
      ...anchor,
      start: Math.min(...present.map((member) => member.start)),
      end: Math.max(...present.map((member) => member.end)),
      text: parts.map((part) => part.text).join(" "),
      parts,
      // A contradiction flag can attach to any member, not only the anchor;
      // the merged row surfaces the first one so merging never hides it.
      inconsistency:
        anchor.inconsistency ??
        present.find((member) => member.inconsistency)?.inconsistency,
    });
  }
  return merged;
}

import type {
  Recap,
  RecapCounts,
  RecapSpeaker,
  RecapStatement,
} from "@/lib/video/recap";

export const counts = (overrides: Partial<RecapCounts> = {}): RecapCounts => ({
  detected: 0,
  credible: 0,
  disputed: 0,
  unverifiable: 0,
  notVerified: 0,
  ...overrides,
});

export const statement = (
  overrides: Partial<RecapStatement> = {},
): RecapStatement => ({
  claimId: "c1",
  at: 95,
  verdict: "disputed",
  flags: ["cherry-picked"],
  quote: "Les salaires ont doublé en cinq ans",
  rationale: "Les séries officielles montrent une hausse de 8 %.",
  sources: [{ title: "INSEE", url: "https://example.org/insee" }],
  ...overrides,
});

export const recapSpeaker = (
  overrides: Partial<RecapSpeaker> = {},
): RecapSpeaker => ({
  label: "A",
  name: "Marine Durand",
  role: "principal",
  nameConfirmed: false,
  speakingTime: 1800,
  turns: 12,
  share: 0.6,
  counts: counts({ detected: 5, credible: 2, disputed: 1, unverifiable: 1, notVerified: 1 }),
  disputed: [statement()],
  credible: [
    statement({ claimId: "c2", at: 130, verdict: "credible", flags: [], quote: "Le chômage a baissé", rationale: "Confirmé par la série trimestrielle." }),
    statement({ claimId: "c3", at: 400, verdict: "credible", flags: [], quote: "La dette a augmenté", rationale: "", sources: [] }),
  ],
  ...overrides,
});

// debateRecap is a moderated two-principal debate: A is named by the analysis,
// B is unnamed, and the moderator F was confirmed by an operator. The
// principals' totals are the sum of the two sheets.
export function debateRecap(overrides: Partial<Recap> = {}): Recap {
  const a = recapSpeaker();
  const b = recapSpeaker({
    label: "B",
    name: "",
    speakingTime: 1200,
    turns: 9,
    share: 0.4,
    counts: counts({ detected: 3, disputed: 2, notVerified: 1 }),
    disputed: [
      statement({ claimId: "b1", at: 610, quote: "La croissance est nulle" }),
      statement({ claimId: "b2", at: 700, quote: "Personne ne paie d'impôts", flags: [] }),
    ],
    credible: [],
  });
  const f = recapSpeaker({
    label: "F",
    name: "Claire Martin",
    role: "moderator",
    nameConfirmed: true,
    speakingTime: 300,
    turns: 20,
    share: 0,
    counts: counts({ detected: 1, credible: 1 }),
    disputed: [],
    credible: [statement({ claimId: "f1", verdict: "credible", flags: [] })],
  });
  return {
    videoId: "vid-1",
    title: "Le grand débat",
    analyzedAt: "2026-11-19T20:00:00Z",
    duration: 10_260,
    speakers: [a, b, f],
    principals: {
      speakingTime: 3000,
      turns: 21,
      counts: counts({ detected: 8, credible: 2, disputed: 3, unverifiable: 1, notVerified: 2 }),
    },
    others: { speakingTime: 300, turns: 20, counts: counts({ detected: 1, credible: 1 }) },
    ...overrides,
  };
}

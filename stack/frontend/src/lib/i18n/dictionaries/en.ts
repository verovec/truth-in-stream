import type { Dictionary } from "./fr";

// English mirrors the French source of truth. The `Dictionary` annotation makes
// a missing or renamed key a compile error; the parity test guards array
// lengths, which the type alone does not.
export const en: Dictionary = {
  meta: {
    title: "jeminforme.fr - Live fact-checking for political debate",
    description:
      "jeminforme.fr checks political claims against trusted sources in real time, so debate stays anchored to the facts.",
  },
  brand: {
    name: "jeminforme.fr",
    tagline: "Fact-checking, live.",
  },
  nav: {
    howItWorks: "How it works",
    mission: "Our mission",
    openApp: "Open the app",
  },
  langSwitch: {
    label: "Choose a language",
    toFrench: "Afficher le site en français",
    toEnglish: "View the site in English",
  },
  hero: {
    eyebrow: "Live fact-checking",
    titleLead: "Political debate,",
    titleAccent: "checked live.",
    subtitle:
      "jeminforme.fr matches every claim to trusted sources, in real time. Debates, rallies and livestreams stay anchored to the facts, not to whoever talks loudest.",
    ctaPrimary: "Open the app",
    ctaSecondary: "See how it works",
    demo: {
      liveLabel: "Live",
      speaker: "Speaker",
      timestamp: "14:32",
      claim: "Two million jobs have been created since 2017.",
      verdictLabel: "Verdict",
      verdict: "Needs nuance",
      verdictNote:
        "Payroll employment did rise over the period, but the figure blends several different measures.",
      sourcesLabel: "Sources",
      sources: [
        { name: "INSEE", detail: "Payroll employment, quarterly series" },
        { name: "DARES", detail: "Labour-market statistics" },
      ],
    },
  },
  pillars: {
    title: "Built for trust",
    items: [
      {
        title: "Sources attached",
        body: "Every verdict cites the references behind it. Nothing is asserted without evidence.",
      },
      {
        title: "In real time",
        body: "Claims are checked as they are spoken, not the next day.",
      },
      {
        title: "On facts, not people",
        body: "Opinions and small talk are left alone: only verifiable claims get a verdict.",
      },
    ],
  },
  how: {
    eyebrow: "How it works",
    title: "Three steps, running continuously",
    subtitle: "As long as someone is speaking, the checking keeps running.",
    steps: [
      {
        index: "01",
        title: "Listen",
        body: "Live audio is transcribed on the fly, sentence by sentence, with no waiting for the broadcast to end.",
      },
      {
        index: "02",
        title: "Retrieve",
        body: "Each verifiable claim is matched against a corpus of trusted sources and established fact-checks.",
      },
      {
        index: "03",
        title: "Verdict",
        body: "A verdict appears within seconds, its sources attached: what holds up, what misleads, what needs nuance.",
      },
    ],
  },
  mission: {
    eyebrow: "Our mission",
    title: "Staying informed is a responsibility.",
    body: "A democracy needs citizens who can tell the true from the merely plausible. jeminforme.fr puts fact-checking within everyone's reach, in French, at the moment the debate happens, so each person can form an opinion on facts rather than volume.",
    points: [
      "Independent and non-partisan",
      "Transparent about its sources",
      "Built for French and European debate",
    ],
  },
  closing: {
    title: "Ready to check the next livestream?",
    body: "Open the analyser, play a stream, and watch verdicts arrive with their sources.",
    cta: "Open the app",
  },
  footer: {
    tagline: "Fact-checking, live.",
    madeIn: "Built in France, for democratic debate.",
    rights: "2026 jeminforme.fr",
    links: [
      { label: "How it works", href: "#comment-ca-marche" },
      { label: "Our mission", href: "#mission" },
      { label: "Open the app", href: "/login" },
    ],
  },
};

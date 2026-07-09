import type { Dictionary } from "./fr";

// English mirrors the French source of truth. The `Dictionary` annotation makes
// a missing or renamed key a compile error; the parity test guards array
// lengths, which the type alone does not.
export const en: Dictionary = {
  meta: {
    title: "jeminforme.fr — Live fact-checking for political debate",
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
  app: {
    header: {
      signOut: "Sign out",
    },
    nav: {
      ariaLabel: "Primary navigation",
      videos: "Videos",
      documents: "Documents",
    },
    documents: {
      heading: "Documents",
      loadingAria: "Loading documents",
      loadError: "Documents could not load: {message}",
      loadErrorFallback: "Documents could not load.",
      retry: "Retry",
      empty: "No documents yet.",
      emptyAdmin: "No documents yet. Upload a PDF to get started.",
      pageCount: { one: "{count} page", other: "{count} pages" },
      status: { ready: "Ready", pending: "Pending", failed: "Failed" },
      analysis: {
        none: "Not analyzed",
        analysing: "Analyzing",
        complete: "Analyzed",
        failed: "Analysis failed",
      },
      counts: {
        credible: { one: "{count} credible", other: "{count} credible" },
        disputed: { one: "{count} disputed", other: "{count} disputed" },
      },
      uploader: {
        prompt: "Drop a PDF here, or click to choose",
        formats: "Text-layer PDF (scanned documents are not supported)",
        inputAria: "Upload a PDF document",
        extracting: "Extracting text…",
        preparing: "Preparing…",
        finalizing: "Finalizing…",
        dismiss: "Dismiss",
        uploadingAria: "Uploading {title}",
        errors: {
          unsupported: "Unsupported file type. Upload a PDF.",
          scanned:
            "This PDF has no extractable text (a scanned document). Upload a text-layer PDF.",
          tooLong:
            "This document exceeds the {max}-sentence limit. Upload a shorter document.",
          failed: "The upload failed.",
        },
      },
    },
    summary: {
      heading: "Live findings",
      ariaLabel: "Live findings summary",
      idleHint: "Findings appear here as the video is analysed.",
      inProgress: "{count} in progress",
      stats: {
        checked: "Checked",
        corroborates: "Corroborated",
        contradicts: "Contradicted",
        unclear: "Unclear",
        unverifiable: "Unverifiable",
        evidence: "Evidence",
        skipped: "Not checked",
      },
    },
    connection: {
      live: "Live",
      reconnecting: "Reconnecting",
      interrupted: "Interrupted",
    },
    speakers: {
      heading: "Speaker reliability",
      speaker: "Speaker",
      claim: { one: "checkable claim", other: "checkable claims" },
      credible: { one: "credible", other: "credible" },
      disputed: { one: "disputed", other: "disputed" },
      unverifiable: { one: "unverifiable", other: "unverifiable" },
      framing: { one: "misleading framing", other: "misleading framings" },
    },
    panel: {
      heading: "Live analysis",
      subtitles: "Subtitles",
      factChecks: "Fact checks",
      subtitlesAria: "Live subtitles",
      separator: "Resize subtitles and fact checks",
      interrupted:
        "Live analysis was interrupted. Playback continues; press play again to retry.",
      reconnecting: "Connection lost. Reconnecting…",
      hints: {
        connecting: "Connecting to live analysis…",
        listening: "Listening for spoken claims…",
        ended: "The stream ended with no checked statements.",
        idle: "Fact checks stream here while the video plays.",
      },
    },
    subtitles: {
      transcriptAria: "Subtitle transcript",
      speaker: "Speaker",
      checking: "Checking this statement…",
      checkFailed: "This statement could not be checked.",
      notChecked: "Not checked — {reason}.",
      skipReasons: {
        notAClaim: "no verifiable claim",
        notCovered: "not covered by the reference corpus",
        notChecked: "the live checker was busy",
        unknown: "an unrecognised reason",
      },
      noMatch: "No confident match.",
      corroborated: "corroborated by the reference corpus",
      match: { one: "match", other: "matches" },
      supporting: "supporting",
      contradicting: "contradicting",
      contradictsEarlier: "Contradicts an earlier statement",
      bySpeaker: "by this speaker:",
    },
    factChecks: {
      resultsAria: "Fact-check results",
      empty: "Fact-checks appear here as claims are verified.",
      placeholderHint: "Fact checks stream here while the video plays.",
    },
    claims: {
      listAria: "Atomic claims",
      pending: "Waiting to be checked…",
      checking: "Checking…",
      unchecked: "Not checked — the checker was at full capacity.",
      error: "This claim could not be checked.",
      showDetail: "Show detail",
      hideDetail: "Hide detail",
      verdicts: {
        credible: "Credible",
        disputed: "Disputed",
        unverifiable: "Unverifiable",
      },
      literal: {
        accurate: "Accurate",
        inaccurate: "Inaccurate",
        unverifiable: "Unverifiable",
      },
      flagsAria: "Manipulation flags",
      flags: {
        "missing-context": "Missing context",
        "cherry-picked": "Cherry-picked",
        outdated: "Outdated",
        misattributed: "Misattributed",
        "misleading-causation": "Misleading causation",
      },
      sources: {
        curated: "curated source",
        verified: "verified from evidence",
      },
      noDirectSource: "no direct source",
      sourcePrefix: "Source: {label}",
      primarySourceAria: "Primary source",
    },
    legacy: {
      verdicts: {
        corroborates: "Corroborates",
        contradicts: "Contradicts",
        unclear: "Unclear",
      },
      evidence: "Evidence",
      similarity: "{percent}% match",
      wikipedia: "Wikipedia",
    },
    player: {
      playError: "This video could not be played.",
      playErrorHint: "The media source did not load. Try selecting it again.",
      loadingAria: "Loading {title}",
      loadError: "This video could not be loaded.",
      idle: "Select a video from the library to play it.",
    },
    library: {
      heading: "Library",
      loadingAria: "Loading library",
      loadError: "The library could not load: {message}",
      loadErrorFallback: "The library could not load.",
      retry: "Try again",
      empty: "No videos yet. Upload one to get started.",
      status: { ready: "Ready", pending: "Processing", failed: "Failed" },
      kind: { sample: "Sample", upload: "Upload", youtube: "YouTube" },
    },
    uploader: {
      prompt: "Drag a video here, or click to choose",
      formats: "MP4, WebM, OGG, or MOV",
      inputAria: "Upload a video",
      preparing: "Preparing…",
      finalizing: "Finalizing…",
      dismiss: "Dismiss",
      uploadingAria: "Uploading {title}",
      unsupported: "Unsupported file type. Upload an MP4, WebM, OGG, or MOV video.",
      failed: "Upload failed.",
    },
    youtube: {
      label: "YouTube URL",
      placeholder: "https://youtu.be/…",
      add: "Add",
      adding: "Adding…",
      invalid: "Enter a YouTube link, e.g. https://youtu.be/…",
      failed: "Could not add this video.",
    },
    exports: {
      heading: "Admin exports",
      transcript: "Transcript (.srt)",
      transcriptPending: "Preparing transcript…",
      claims: "Claims (.csv)",
      claimsPending: "Preparing claims…",
      missingSnapshot:
        "No cached analysis for this video. Re-run analysis to repopulate the export cache.",
      failed: "Export failed. Please try again.",
    },
    debug: {
      show: "Debug",
      hide: "Hide debug",
    },
  },
  login: {
    metaTitle: "Sign in — jeminforme.fr",
    intro: "Sign in to continue",
    signIn: "Sign in with Keycloak",
    modalTitle: "Sign in",
    modalIntro: "Sign in to open the analyser",
    close: "Close",
    errors: {
      session: "Your sign-in session expired. Please try again.",
      exchange: "Sign-in could not be completed. Please try again.",
    },
  },
};

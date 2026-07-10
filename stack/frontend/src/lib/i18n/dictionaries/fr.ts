// French is the source of truth for the dictionary shape. `Dictionary` is
// derived from this object, so `en` is checked against it at compile time and
// any missing or renamed key is a build error.
export const fr = {
  meta: {
    title:
      "jeminforme.fr — Vérification des faits en direct pour le débat politique",
    description:
      "jeminforme.fr confronte les affirmations politiques à des sources fiables, en temps réel, pour que le débat reste ancré aux faits.",
  },
  brand: {
    name: "jeminforme.fr",
    tagline: "La vérification des faits, en direct.",
  },
  nav: {
    howItWorks: "Comment ça marche",
    mission: "Notre mission",
    openApp: "Ouvrir l'application",
  },
  langSwitch: {
    label: "Choisir la langue",
    toFrench: "Afficher le site en français",
    toEnglish: "View the site in English",
  },
  hero: {
    eyebrow: "Vérification des faits en direct",
    titleLead: "Le débat politique,",
    titleAccent: "vérifié en direct.",
    subtitle:
      "jeminforme.fr confronte chaque affirmation à des sources fiables, en temps réel. Débats, meetings et directs restent ancrés aux faits, pas à celui qui parle le plus fort.",
    ctaPrimary: "Ouvrir l'application",
    ctaSecondary: "Voir comment ça marche",
    demo: {
      liveLabel: "En direct",
      speaker: "Intervenant",
      timestamp: "14:32",
      claim: "Deux millions d'emplois ont été créés depuis 2017.",
      verdictLabel: "Verdict",
      verdict: "À nuancer",
      verdictNote:
        "L'emploi salarié progresse sur la période, mais le total agrège des mesures différentes.",
      sourcesLabel: "Sources",
      sources: [
        { name: "INSEE", detail: "Emploi salarié, séries trimestrielles" },
        { name: "DARES", detail: "Statistiques du marché du travail" },
      ],
    },
  },
  pillars: {
    title: "Conçu pour la confiance",
    items: [
      {
        title: "Sources à l'appui",
        body: "Chaque verdict cite les références qui le fondent. Rien n'est affirmé sans preuve.",
      },
      {
        title: "En temps réel",
        body: "Les affirmations sont vérifiées à mesure qu'elles sont prononcées, pas le lendemain.",
      },
      {
        title: "Sur les faits, pas les personnes",
        body: "Les opinions et les échanges sont laissés de côté : seules les affirmations vérifiables reçoivent un verdict.",
      },
    ],
  },
  how: {
    eyebrow: "Comment ça marche",
    title: "Trois étapes, en continu",
    subtitle: "Tant que quelqu'un parle, la vérification tourne.",
    steps: [
      {
        index: "01",
        title: "Écoute",
        body: "L'audio du direct est transcrit à la volée, phrase après phrase, sans attendre la fin de l'émission.",
      },
      {
        index: "02",
        title: "Recherche",
        body: "Chaque affirmation vérifiable est confrontée à un corpus de sources fiables et de vérifications déjà établies.",
      },
      {
        index: "03",
        title: "Verdict",
        body: "Un verdict apparaît en quelques secondes, ses sources attachées : ce qui tient, ce qui trompe, ce qui reste à nuancer.",
      },
    ],
  },
  mission: {
    eyebrow: "Notre mission",
    title: "S'informer est une responsabilité.",
    body: "Une démocratie a besoin de citoyens capables de distinguer le vrai du plausible. jeminforme.fr met la vérification à la portée de tous, en français, au moment où le débat a lieu, pour que chacun se forge une opinion sur des faits et non sur le volume.",
    points: [
      "Indépendant et non partisan",
      "Transparent sur ses sources",
      "Pensé pour le débat français et européen",
    ],
  },
  closing: {
    title: "Prêt à vérifier le prochain direct ?",
    body: "Ouvrez l'analyseur, lancez un flux, et regardez les verdicts arriver avec leurs sources.",
    cta: "Ouvrir l'application",
  },
  footer: {
    tagline: "La vérification des faits, en direct.",
    madeIn: "Conçu en France, pour le débat démocratique.",
    rights: "2026 jeminforme.fr",
    links: [
      { label: "Comment ça marche", href: "#comment-ca-marche" },
      { label: "Notre mission", href: "#mission" },
      { label: "Ouvrir l'application", href: "/login" },
    ],
  },
  // The authenticated analyser at /app. Templates use {name} placeholders
  // resolved by formatTemplate; countable nouns carry one/other forms picked by
  // the plural helper with the locale's own rule (French counts zero as
  // singular, English does not).
  app: {
    header: {
      signOut: "Se déconnecter",
    },
    nav: {
      ariaLabel: "Navigation principale",
      videos: "Vidéos",
      documents: "Documents",
      backoffice: "Back-office",
    },
    backoffice: {
      heading: "Back-office",
      intro:
        "Espace réservé aux administrateurs pour alimenter la bibliothèque analysée.",
      videos: {
        heading: "Vidéos",
        description:
          "Importez des vidéos et des liens YouTube, et gérez la bibliothèque.",
        list: {
          heading: "Bibliothèque vidéo",
          empty: "Aucune vidéo pour le moment.",
          delete: "Supprimer",
          confirm: "Supprimer cette vidéo ? Cette action est irréversible.",
          confirmYes: "Confirmer",
          confirmNo: "Annuler",
          deleting: "Suppression…",
          deleteError: "La suppression a échoué : {message}",
          deleteErrorFallback: "La suppression a échoué.",
        },
      },
      documents: {
        heading: "Documents",
        description: "Importez des PDF à analyser.",
      },
    },
    documents: {
      heading: "Documents",
      loadingAria: "Chargement des documents",
      loadError: "Les documents n'ont pas pu se charger : {message}",
      loadErrorFallback: "Les documents n'ont pas pu se charger.",
      retry: "Réessayer",
      empty: "Aucun document pour le moment.",
      emptyAdmin: "Aucun document pour le moment. Importez un PDF pour commencer.",
      pageCount: { one: "{count} page", other: "{count} pages" },
      status: { ready: "Prêt", pending: "En cours", failed: "Échec" },
      analysis: {
        none: "Non analysé",
        analysing: "Analyse en cours",
        complete: "Analysé",
        failed: "Échec de l'analyse",
      },
      counts: {
        credible: { one: "{count} fiable", other: "{count} fiables" },
        disputed: { one: "{count} contesté", other: "{count} contestés" },
      },
      uploader: {
        prompt: "Déposez un PDF ici, ou cliquez pour choisir",
        formats: "PDF avec texte (les documents numérisés ne sont pas pris en charge)",
        inputAria: "Importer un document PDF",
        extracting: "Extraction du texte…",
        preparing: "Préparation…",
        finalizing: "Finalisation…",
        dismiss: "Fermer",
        uploadingAria: "Envoi de {title}",
        errors: {
          unsupported: "Type de fichier non pris en charge. Importez un PDF.",
          scanned:
            "Ce PDF ne contient pas de texte extractible (document numérisé). Importez un PDF avec du texte.",
          tooLong:
            "Ce document dépasse la limite de {max} phrases. Importez un document plus court.",
          failed: "L'import a échoué.",
        },
      },
    },
    viewer: {
      back: "Documents",
      loadingAria: "Chargement du document",
      loadError: "Ce document n'a pas pu se charger : {message}",
      loadErrorFallback: "Ce document n'a pas pu se charger.",
      retry: "Réessayer",
      pdf: {
        loading: "Chargement du PDF…",
        error: "Le PDF n'a pas pu s'afficher.",
        unavailable: "Le PDF n'est pas encore disponible.",
        documentAria: "Document PDF",
        page: "Page {page}",
      },
      progress: {
        analysing: "Analyse en cours",
        counter: "{processed} / {total} phrases",
        complete: "Analyse terminée",
        none: "Non analysé",
        failed: "L'analyse a échoué.",
      },
      panel: {
        heading: "Vérifications",
        ariaLabel: "Phrases analysées",
        empty: "Aucune phrase analysée pour le moment.",
        pending: "En attente de vérification…",
        error: "Cette phrase n'a pas pu être vérifiée.",
        page: "p. {page}",
        skipReasons: {
          not_a_claim: "Aucune affirmation vérifiable",
          not_covered: "Hors du corpus de référence",
        },
      },
      highlight: {
        layerAria: "Phrases surlignées",
        aria: "{verdict} : {snippet}",
      },
      reanalyse: {
        action: "Relancer l'analyse",
        retry: "Réessayer l'analyse",
        confirm:
          "Relancer l'analyse ? Les résultats précédents seront remplacés.",
        confirmYes: "Confirmer",
        confirmNo: "Annuler",
        running: "Analyse en cours…",
        errors: {
          conflict: "Une analyse est déjà en cours.",
          disabled: "L'analyse n'est pas disponible.",
          failed: "L'analyse n'a pas pu démarrer.",
        },
      },
    },
    summary: {
      heading: "Constats en direct",
      ariaLabel: "Résumé des constats en direct",
      idleHint: "Les constats apparaissent ici pendant l'analyse de la vidéo.",
      inProgress: "{count} en cours",
      stats: {
        checked: "Vérifiées",
        corroborates: "Corroborées",
        contradicts: "Contredites",
        unclear: "Incertaines",
        unverifiable: "Invérifiables",
        evidence: "Preuves",
        skipped: "Non vérifiées",
      },
    },
    connection: {
      live: "En direct",
      reconnecting: "Reconnexion",
      interrupted: "Interrompue",
    },
    speakers: {
      heading: "Fiabilité des intervenants",
      speaker: "Intervenant",
      claim: { one: "affirmation vérifiable", other: "affirmations vérifiables" },
      credible: { one: "crédible", other: "crédibles" },
      disputed: { one: "contestée", other: "contestées" },
      unverifiable: { one: "invérifiable", other: "invérifiables" },
      framing: { one: "cadrage trompeur", other: "cadrages trompeurs" },
    },
    panel: {
      heading: "Analyse en direct",
      subtitles: "Sous-titres",
      factChecks: "Vérifications",
      subtitlesAria: "Sous-titres en direct",
      separator: "Redimensionner les sous-titres et les vérifications",
      interrupted:
        "L'analyse en direct a été interrompue. La lecture continue ; relancez la lecture pour réessayer.",
      reconnecting: "Connexion perdue. Reconnexion…",
      hints: {
        connecting: "Connexion à l'analyse en direct…",
        listening: "En écoute des affirmations prononcées…",
        ended: "Le direct s'est terminé sans déclaration vérifiée.",
        idle: "Les vérifications apparaissent ici pendant la lecture de la vidéo.",
      },
    },
    subtitles: {
      transcriptAria: "Transcription des sous-titres",
      speaker: "Intervenant",
      checking: "Vérification de cette déclaration…",
      checkFailed: "Cette déclaration n'a pas pu être vérifiée.",
      notChecked: "Non vérifiée — {reason}.",
      skipReasons: {
        notAClaim: "aucune affirmation vérifiable",
        notCovered: "hors du corpus de référence",
        notChecked: "le vérificateur en direct était occupé",
        unknown: "raison inconnue",
      },
      noMatch: "Aucune correspondance fiable.",
      corroborated: "corroborée par le corpus de référence",
      match: { one: "correspondance", other: "correspondances" },
      supporting: "pour",
      contradicting: "contre",
      contradictsEarlier: "Contredit une déclaration antérieure",
      bySpeaker: "du même intervenant :",
    },
    factChecks: {
      resultsAria: "Résultats des vérifications",
      empty:
        "Les vérifications apparaissent ici à mesure que les affirmations sont contrôlées.",
      placeholderHint:
        "Les vérifications défilent ici pendant la lecture de la vidéo.",
    },
    claims: {
      listAria: "Affirmations atomiques",
      pending: "En attente de vérification…",
      checking: "Vérification…",
      unchecked: "Non vérifiée — le vérificateur était à pleine capacité.",
      error: "Cette affirmation n'a pas pu être vérifiée.",
      showDetail: "Voir le détail",
      hideDetail: "Masquer le détail",
      verdicts: {
        credible: "Fiable",
        disputed: "Contesté",
        unverifiable: "Invérifiable",
      },
      literal: {
        accurate: "Exact",
        inaccurate: "Inexact",
        unverifiable: "Invérifiable",
      },
      flagsAria: "Drapeaux de manipulation",
      flags: {
        "missing-context": "Contexte manquant",
        "cherry-picked": "Données triées",
        outdated: "Périmé",
        misattributed: "Mal attribué",
        "misleading-causation": "Causalité trompeuse",
      },
      sources: {
        curated: "source vérifiée",
        verified: "vérifié sur preuves",
      },
      noDirectSource: "sans source directe",
      sourcePrefix: "Source : {label}",
      primarySourceAria: "Source principale",
    },
    legacy: {
      verdicts: {
        corroborates: "Corrobore",
        contradicts: "Contredit",
        unclear: "Incertain",
      },
      evidence: "Preuve",
      similarity: "{percent}% de similarité",
      wikipedia: "Wikipédia",
    },
    player: {
      playError: "Cette vidéo n'a pas pu être lue.",
      playErrorHint:
        "La source média ne s'est pas chargée. Essayez de la sélectionner à nouveau.",
      loadingAria: "Chargement de {title}",
      loadError: "Cette vidéo n'a pas pu être chargée.",
      idle: "Sélectionnez une vidéo de la bibliothèque pour la lire.",
    },
    library: {
      heading: "Bibliothèque",
      loadingAria: "Chargement de la bibliothèque",
      loadError: "La bibliothèque n'a pas pu se charger : {message}",
      loadErrorFallback: "La bibliothèque n'a pas pu se charger.",
      retry: "Réessayer",
      empty: "Aucune vidéo pour le moment. Importez-en une pour commencer.",
      status: { ready: "Prête", pending: "En cours", failed: "Échec" },
      kind: { sample: "Exemple", upload: "Import", youtube: "YouTube" },
    },
    uploader: {
      prompt: "Déposez une vidéo ici, ou cliquez pour choisir",
      formats: "MP4, WebM, OGG ou MOV",
      inputAria: "Importer une vidéo",
      preparing: "Préparation…",
      finalizing: "Finalisation…",
      dismiss: "Fermer",
      uploadingAria: "Envoi de {title}",
      unsupported:
        "Type de fichier non pris en charge. Importez une vidéo MP4, WebM, OGG ou MOV.",
      failed: "L'envoi a échoué.",
    },
    youtube: {
      label: "URL YouTube",
      placeholder: "https://youtu.be/…",
      add: "Ajouter",
      adding: "Ajout…",
      invalid: "Saisissez un lien YouTube, p. ex. https://youtu.be/…",
      failed: "Cette vidéo n'a pas pu être ajoutée.",
    },
    exports: {
      heading: "Exports administrateur",
      transcript: "Transcription (.srt)",
      transcriptPending: "Préparation de la transcription…",
      claims: "Affirmations (.csv)",
      claimsPending: "Préparation des affirmations…",
      missingSnapshot:
        "Aucune analyse en cache pour cette vidéo. Relancez l'analyse pour regénérer l'export.",
      failed: "L'export a échoué. Veuillez réessayer.",
    },
    debug: {
      show: "Debug",
      hide: "Masquer le debug",
    },
  },
  login: {
    metaTitle: "Connexion — jeminforme.fr",
    intro: "Connectez-vous pour continuer",
    signIn: "Se connecter avec Keycloak",
    modalTitle: "Connexion",
    modalIntro: "Connectez-vous pour ouvrir l'analyseur",
    close: "Fermer",
    errors: {
      session: "Votre session de connexion a expiré. Veuillez réessayer.",
      exchange: "La connexion n'a pas pu aboutir. Veuillez réessayer.",
    },
  },
};

export type Dictionary = typeof fr;

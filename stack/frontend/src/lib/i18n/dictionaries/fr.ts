// French is the source of truth for the dictionary shape. `Dictionary` is
// derived from this object, so `en` is checked against it at compile time and
// any missing or renamed key is a build error.
export const fr = {
  meta: {
    title:
      "jeminforme.fr - Vérification des faits en direct pour le débat politique",
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
};

export type Dictionary = typeof fr;

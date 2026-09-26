// Everything the page says lives in one Content object per language, so
// copy edits never touch components and the two languages cannot drift in
// structure: a missing field is a type error.

export type Lang = "en" | "zh";

/** A comparison cell: a verdict plus an optional short qualifier. */
export type Verdict = "yes" | "no" | "partial";
export interface Cell {
  v: Verdict;
  note?: string;
}

export interface Step {
  title: string;
  body: string;
  code?: string;
  /** Language hint shown on the code block. */
  codeLabel?: string;
  after?: string;
}

export interface DiagramPhase {
  label: string;
  caption: string;
}

export interface Content {
  meta: { title: string; description: string };
  nav: { demo: string; how: string; compare: string; start: string; faq: string; github: string };
  hero: {
    eyebrow: string;
    title: string;
    titleAccent: string;
    lead: string;
    ctaPrimary: string;
    ctaSecondary: string;
    install: string;
    facts: string[];
  };
  demo: {
    title: string;
    lead: string;
    left: string;
    right: string;
    chapters: { start: string; switch: string; outage: string; back: string };
    note: string;
    reproduce: string;
  };
  fit: {
    title: string;
    yesTitle: string;
    yes: string[];
    noTitle: string;
    no: string[];
  };
  how: {
    title: string;
    lead: string;
    laptop: string;
    server: string;
    delivered: string;
    buffer: string;
    lost: string;
    phases: { normal: DiagramPhase; switch: DiagramPhase; outage: DiagramPhase; resume: DiagramPhase };
    play: string;
    pause: string;
    points: { title: string; body: string }[];
  };
  compare: {
    title: string;
    lead: string;
    tools: string[];
    rows: { label: string; cells: Cell[] }[];
    footnote: string;
  };
  start: {
    title: string;
    lead: string;
    steps: Step[];
    more: string;
  };
  security: { title: string; body: string; link: string };
  evidence: {
    title: string;
    lead: string;
    stats: { value: string; label: string; detail: string }[];
    link: string;
  };
  faq: { title: string; items: { q: string; a: string }[] };
  footer: { license: string; docs: string; madeWith: string };
  copy: { copy: string; copied: string };
}

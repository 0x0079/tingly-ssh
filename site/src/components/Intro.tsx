import type { Content } from "../content/types";

/** The problem and the idea, in a few paragraphs of plain prose. */
export function Intro({ t }: { t: Content["intro"] }) {
  return (
    <section className="intro" aria-label="Introduction">
      <div className="prose">
        {t.paragraphs.map((p) => (
          <p key={p}>{p}</p>
        ))}
      </div>
      <ul className="wide facts">
        {t.facts.map((f) => (
          <li key={f}>{f}</li>
        ))}
      </ul>
    </section>
  );
}

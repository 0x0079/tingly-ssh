import type { ReactNode } from "react";

interface Props {
  id: string;
  /** Shown above the title as a small section number, e.g. "01". */
  index: number;
  title: string;
  lead?: string;
  children: ReactNode;
}

/**
 * A page section: a numbered heading and lead in the reading column. The
 * children pick their own width (.prose, .wide), like figures in an article.
 */
export function Section({ id, index, title, lead, children }: Props) {
  return (
    <section id={id} className="section" aria-labelledby={`${id}-title`}>
      <header className="prose section__head">
        <p className="eyebrow">{String(index).padStart(2, "0")}</p>
        <h2 id={`${id}-title`} className="section__title">
          {title}
        </h2>
        {lead && <p className="section__lead">{lead}</p>}
      </header>
      {children}
    </section>
  );
}

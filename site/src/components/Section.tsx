import type { ReactNode } from "react";

interface Props {
  id: string;
  title: string;
  lead?: string;
  children: ReactNode;
  tone?: "plain" | "muted";
}

/** A page section with a consistent heading, lead paragraph and width. */
export function Section({ id, title, lead, children, tone = "plain" }: Props) {
  return (
    <section id={id} className={`section section--${tone}`} aria-labelledby={`${id}-title`}>
      <div className="container">
        <h2 id={`${id}-title`} className="section__title">
          {title}
        </h2>
        {lead && <p className="section__lead">{lead}</p>}
        {children}
      </div>
    </section>
  );
}

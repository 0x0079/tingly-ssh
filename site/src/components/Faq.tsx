import type { Content } from "../content/types";
import { Section } from "./Section";

export function Faq({ t }: { t: Content["faq"] }) {
  return (
    <Section id="faq" title={t.title} tone="muted">
      <div className="faq">
        {t.items.map((item) => (
          <details key={item.q} className="faq__item">
            <summary>{item.q}</summary>
            <p>{item.a}</p>
          </details>
        ))}
      </div>
    </Section>
  );
}

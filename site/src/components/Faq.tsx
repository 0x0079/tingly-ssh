import type { Content } from "../content/types";
import { Section } from "./Section";

export function Faq({ t, index }: { t: Content["faq"]; index: number }) {
  return (
    <Section id="faq" index={index} title={t.title}>
      <div className="prose faq">
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

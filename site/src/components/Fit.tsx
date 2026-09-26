import type { Content } from "../content/types";
import { Section } from "./Section";

export function Fit({ t }: { t: Content["fit"] }) {
  return (
    <Section id="fit" title={t.title} tone="muted">
      <div className="fit">
        <div className="card fit__col fit__col--yes">
          <h3>{t.yesTitle}</h3>
          <ul>
            {t.yes.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ul>
        </div>
        <div className="card fit__col fit__col--no">
          <h3>{t.noTitle}</h3>
          <ul>
            {t.no.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ul>
        </div>
      </div>
    </Section>
  );
}

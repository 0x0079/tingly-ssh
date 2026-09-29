import type { Content } from "../content/types";
import { Section } from "./Section";

export function Fit({ t, index }: { t: Content["fit"]; index: number }) {
  return (
    <Section id="fit" index={index} title={t.title}>
      <div className="wide fit">
        <div className="fit__col fit__col--yes">
          <h3>{t.yesTitle}</h3>
          <ul>
            {t.yes.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ul>
        </div>
        <div className="fit__col fit__col--no">
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

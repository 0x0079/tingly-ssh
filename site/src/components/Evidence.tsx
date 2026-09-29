import type { Content } from "../content/types";
import { blob } from "../content/code";
import { Section } from "./Section";

interface Props {
  t: Content["evidence"];
  security: Content["security"];
  index: number;
}

export function Evidence({ t, security, index }: Props) {
  return (
    <Section id="evidence" index={index} title={t.title} lead={t.lead}>
      <dl className="wide stats">
        {t.stats.map((s) => (
          <div key={s.label} className="stat">
            <dt>
              <span className="stat__value">{s.value}</span>
              <span className="stat__label">{s.label}</span>
            </dt>
            <dd className="stat__detail">{s.detail}</dd>
          </div>
        ))}
      </dl>
      <p className="wide more">
        <a href={blob("docs/07-verification-plan.md")}>{t.link} →</a>
      </p>
      <aside className="prose callout">
        <h3>{security.title}</h3>
        <p>{security.body}</p>
        <a href={blob("docs/04-security-model.md")}>{security.link} →</a>
      </aside>
    </Section>
  );
}

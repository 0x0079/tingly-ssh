import type { Content } from "../content/types";
import { blob } from "../content/code";
import { Section } from "./Section";

export function Evidence({ t, security }: { t: Content["evidence"]; security: Content["security"] }) {
  return (
    <Section id="evidence" title={t.title} lead={t.lead}>
      <div className="stats">
        {t.stats.map((s) => (
          <div key={s.label} className="card stat">
            <div className="stat__value">{s.value}</div>
            <div className="stat__label">{s.label}</div>
            <p className="stat__detail">{s.detail}</p>
          </div>
        ))}
      </div>
      <p className="more">
        <a href={blob("docs/07-verification-plan.md")}>{t.link} →</a>
      </p>
      <div className="card security">
        <h3>{security.title}</h3>
        <p>{security.body}</p>
        <a href={blob("docs/04-security-model.md")}>{security.link} →</a>
      </div>
    </Section>
  );
}

import type { Content } from "../content/types";
import { blob } from "../content/code";
import { CodeBlock } from "./CodeBlock";
import { Section } from "./Section";

export function QuickStart({ t, copy }: { t: Content["start"]; copy: Content["copy"] }) {
  return (
    <Section id="start" title={t.title} lead={t.lead} tone="muted">
      <ol className="steps">
        {t.steps.map((s, i) => (
          <li key={s.title} className="step">
            <div className="step__num" aria-hidden="true">
              {i + 1}
            </div>
            <div className="step__body">
              <h3>{s.title}</h3>
              <p>{s.body}</p>
              {s.code && <CodeBlock code={s.code} label={s.codeLabel} copyText={copy} />}
              {s.after && <p className="step__after">{s.after}</p>}
            </div>
          </li>
        ))}
      </ol>
      <p className="more">
        <a href={blob("docs/09-migrating-from-ssh.md")}>{t.more} →</a>
      </p>
    </Section>
  );
}

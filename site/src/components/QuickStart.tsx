import type { Content } from "../content/types";
import { blob } from "../content/code";
import { CodeBlock } from "./CodeBlock";
import { Section } from "./Section";

interface Props {
  t: Content["start"];
  copy: Content["copy"];
  index: number;
}

export function QuickStart({ t, copy, index }: Props) {
  return (
    <Section id="start" index={index} title={t.title} lead={t.lead}>
      <ol className="prose steps">
        {t.steps.map((s, i) => (
          <li key={s.title} className="step">
            <span className="step__num" aria-hidden="true">
              {i + 1}
            </span>
            <h3>{s.title}</h3>
            <p>{s.body}</p>
            {s.code && <CodeBlock code={s.code} label={s.codeLabel} copyText={copy} />}
            {s.after && <p className="step__after">{s.after}</p>}
          </li>
        ))}
      </ol>
      <p className="prose more">
        <a href={blob("docs/09-migrating-from-ssh.md")}>{t.more} →</a>
      </p>
    </Section>
  );
}

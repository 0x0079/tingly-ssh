import type { Content } from "../content/types";

const bar = (pct: number) => {
  const n = Math.round(pct / 10);
  return "█".repeat(n) + "░".repeat(10 - n);
};

export function Hero({ t }: { t: Content["hero"] }) {
  return (
    <section className="hero" id="top">
      <div className="container hero__grid">
        <div className="hero__copy">
          <p className="hero__eyebrow">{t.eyebrow}</p>
          <h1 className="hero__title">
            {t.title} <span className="hero__accent">{t.titleAccent}</span>
          </h1>
          <p className="hero__lead">{t.lead}</p>
          <div className="hero__ctas">
            <a className="btn btn--primary" href="#start">
              {t.ctaPrimary}
            </a>
            <a className="btn" href="#demo">
              {t.ctaSecondary}
            </a>
          </div>
          <ul className="hero__facts">
            {t.facts.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
          <p className="hero__install">{t.install}</p>
        </div>

        <figure className="hero__term" aria-hidden="true">
          <div className="term__event">⚡ {t.term.outage}</div>
          <div className="term term--bad">
            <div className="term__head">
              <span className="term__dot" />
              {t.term.plain}
            </div>
            <pre>
              <span className="term__dim">$ ssh myserver</span>
              {"\n"}job {bar(15)} 15%{"\n"}
              <span className="term__err">client_loop: send disconnect: Broken pipe</span>
              {"\n"}
              <span className="term__dim">✗ {t.term.lost}</span>
            </pre>
          </div>
          <div className="term term--good">
            <div className="term__head">
              <span className="term__dot" />
              {t.term.tingly}
            </div>
            <pre>
              <span className="term__dim">$ ssh myserver-roam</span>
              {"\n"}job {bar(100)} 100%{"\n"}
              <span className="term__ok">✓ {t.term.ok}</span>
            </pre>
          </div>
        </figure>
      </div>
    </section>
  );
}

import type { Content } from "../content/types";

export function Hero({ t }: { t: Content["hero"] }) {
  return (
    <section className="hero" id="top">
      <div className="container">
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
    </section>
  );
}

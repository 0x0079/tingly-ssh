import type { Content } from "../content/types";
import { ByteStream } from "./ByteStream";

export function Hero({ t }: { t: Content["hero"] }) {
  return (
    <section className="hero" id="top">
      <div className="wide hero__grid">
        <div className="hero__copy">
          <p className="eyebrow">{t.eyebrow}</p>
          <h1 className="hero__title">
            {t.title}
            <br />
            {t.titleAccent}
          </h1>
          <p className="hero__lead">{t.lead}</p>
          <div className="hero__ctas">
            <a className="btn btn--primary" href="#start">
              {t.ctaPrimary}
            </a>
            <a className="btn btn--text" href="#demo">
              {t.ctaSecondary} <span aria-hidden="true">↓</span>
            </a>
          </div>
          <p className="hero__install">{t.install}</p>
        </div>
        <ByteStream t={t.art} />
      </div>
    </section>
  );
}

import type { Content } from "../content/types";
import { DOCS, REPO, blob } from "../content/code";
import { Logo } from "./Logo";

export function Footer({ t }: { t: Content["footer"] }) {
  return (
    <footer className="footer">
      <div className="container footer__inner">
        <div className="footer__brand">
          <Logo size={18} />
          <span>tingly-shell</span>
        </div>
        <p className="footer__made">{t.madeWith}</p>
        <nav className="footer__links">
          <a href={REPO}>GitHub</a>
          <a href={DOCS}>{t.docs}</a>
          <a href={blob("LICENSE")}>{t.license}</a>
        </nav>
      </div>
    </footer>
  );
}

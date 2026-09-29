import type { Content } from "../content/types";
import { DOCS, REPO, blob } from "../content/code";
import { Logo } from "./Logo";

export function Footer({ t }: { t: Content["footer"] }) {
  return (
    <footer className="footer">
      <div className="wide footer__inner">
        <div className="footer__brand">
          <Logo size={20} />
          <span>tingly-ssh</span>
        </div>
        <p className="footer__made">{t.madeWith}</p>
        <nav className="footer__links" aria-label="Project">
          <a href={REPO}>GitHub</a>
          <a href={DOCS}>{t.docs}</a>
          <a href={blob("LICENSE")}>{t.license}</a>
        </nav>
      </div>
    </footer>
  );
}

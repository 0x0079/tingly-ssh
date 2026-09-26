import type { Content, Lang } from "../content/types";
import { REPO } from "../content/code";
import { Logo } from "./Logo";

interface Props {
  t: Content["nav"];
  lang: Lang;
  setLang: (l: Lang) => void;
}

export function Nav({ t, lang, setLang }: Props) {
  const links: [string, string][] = [
    ["#demo", t.demo],
    ["#how", t.how],
    ["#compare", t.compare],
    ["#start", t.start],
    ["#faq", t.faq],
  ];
  return (
    <header className="nav">
      <div className="container nav__inner">
        <a href="#top" className="nav__brand">
          <Logo />
          <span>tingly-ssh</span>
        </a>
        <nav className="nav__links" aria-label="Sections">
          {links.map(([href, label]) => (
            <a key={href} href={href}>
              {label}
            </a>
          ))}
        </nav>
        <div className="nav__actions">
          <button
            type="button"
            className="nav__lang"
            onClick={() => setLang(lang === "en" ? "zh" : "en")}
            aria-label={lang === "en" ? "切换到中文" : "Switch to English"}
          >
            {lang === "en" ? "中文" : "EN"}
          </button>
          <a className="nav__gh" href={REPO}>
            {t.github}
          </a>
        </div>
      </div>
    </header>
  );
}

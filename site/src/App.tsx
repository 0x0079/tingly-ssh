import { useLang } from "./i18n";
import { blob } from "./content/code";
import { Nav } from "./components/Nav";
import { Hero } from "./components/Hero";
import { Intro } from "./components/Intro";
import { Section } from "./components/Section";
import { DemoPlayer } from "./components/DemoPlayer";
import { PullQuote } from "./components/PullQuote";
import { Fit } from "./components/Fit";
import { ResumeDiagram } from "./components/ResumeDiagram";
import { Compare } from "./components/Compare";
import { QuickStart } from "./components/QuickStart";
import { Evidence } from "./components/Evidence";
import { Faq } from "./components/Faq";
import { Footer } from "./components/Footer";

export function App() {
  const { lang, setLang, t } = useLang();
  return (
    <>
      <Nav t={t.nav} lang={lang} setLang={setLang} />
      <main>
        <Hero t={t.hero} />
        <Intro t={t.intro} />

        <Section id="demo" index={1} title={t.demo.title} lead={t.demo.lead}>
          <DemoPlayer t={t.demo} />
          <div className="wide">
            <p className="demo__note">
              {t.demo.note} <a href={blob("demo/record.sh")}>{t.demo.reproduce}</a>
            </p>
          </div>
          <PullQuote text={t.demo.quote.text} cite={t.demo.quote.cite} />
        </Section>

        <Section id="how" index={2} title={t.how.title} lead={t.how.lead}>
          <ResumeDiagram t={t.how} />
          <div className="wide points">
            {t.how.points.map((p) => (
              <div key={p.title} className="point">
                <h3>{p.title}</h3>
                <p>{p.body}</p>
              </div>
            ))}
          </div>
        </Section>

        <Fit t={t.fit} index={3} />
        <Compare t={t.compare} index={4} />
        <QuickStart t={t.start} copy={t.copy} index={5} />
        <Evidence t={t.evidence} security={t.security} index={6} />
        <Faq t={t.faq} index={7} />
      </main>
      <Footer t={t.footer} />
    </>
  );
}

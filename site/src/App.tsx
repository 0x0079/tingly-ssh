import { useLang } from "./i18n";
import { blob } from "./content/code";
import { Nav } from "./components/Nav";
import { Hero } from "./components/Hero";
import { Section } from "./components/Section";
import { DemoPlayer } from "./components/DemoPlayer";
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

        <Section id="demo" title={t.demo.title} lead={t.demo.lead}>
          <DemoPlayer t={t.demo} />
          <p className="demo__note">
            {t.demo.note}{" "}
            <a href={blob("demo/record.sh")}>{t.demo.reproduce}</a>
          </p>
        </Section>

        <Fit t={t.fit} />

        <Section id="how" title={t.how.title} lead={t.how.lead}>
          <ResumeDiagram t={t.how} />
          <div className="points">
            {t.how.points.map((p) => (
              <div key={p.title} className="card point">
                <h3>{p.title}</h3>
                <p>{p.body}</p>
              </div>
            ))}
          </div>
        </Section>

        <Compare t={t.compare} />
        <QuickStart t={t.start} copy={t.copy} />
        <Evidence t={t.evidence} security={t.security} />
        <Faq t={t.faq} />
      </main>
      <Footer t={t.footer} />
    </>
  );
}

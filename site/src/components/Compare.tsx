import type { Cell, Content } from "../content/types";
import { blob } from "../content/code";
import { Section } from "./Section";

const MARK: Record<Cell["v"], string> = { yes: "✓", no: "✕", partial: "◐" };

export function Compare({ t, index }: { t: Content["compare"]; index: number }) {
  return (
    <Section id="compare" index={index} title={t.title} lead={t.lead}>
      <div className="wide table-wrap">
        <table className="compare">
          <thead>
            <tr>
              <th scope="col" />
              {t.tools.map((tool, i) => (
                <th key={tool} scope="col" className={i === 0 ? "compare__us" : undefined}>
                  {tool}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {t.rows.map((row) => (
              <tr key={row.label}>
                <th scope="row">{row.label}</th>
                {row.cells.map((c, i) => (
                  <td key={i} className={`v v--${c.v} ${i === 0 ? "compare__us" : ""}`}>
                    <span className="v__mark" aria-label={c.v}>
                      {MARK[c.v]}
                    </span>
                    {c.note && <span className="v__note">{c.note}</span>}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="wide footnote">
        <a href={blob("docs/adr/0001-transport-choice.md")}>{t.footnote}</a>
      </p>
    </Section>
  );
}

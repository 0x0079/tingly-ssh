import { useRef } from "react";
import type { Content } from "../content/types";
import { useLoopClock } from "../useLoopClock";
import { frameAt, LOOP, PHASE_STARTS, type Route } from "./resumeSim";

// Geometry of the SVG, in viewBox units.
const W = 900;
const H = 320;
const LAPTOP = { x: 20, y: 70, w: 230, h: 180 };
const SERVER = { x: 650, y: 70, w: 230, h: 180 };
const FROM = { x: SERVER.x, y: 160 };
const TO = { x: LAPTOP.x + LAPTOP.w, y: 160 };
const CONTROL: Record<Route, { x: number; y: number }> = {
  wifi: { x: 450, y: 30 },
  cell: { x: 450, y: 290 },
};
const IP: Record<Route, string> = { wifi: "10.0.0.2 · Wi-Fi", cell: "172.20.4.9 · 5G" };

function pointOn(route: Route, progress: number) {
  const s = Math.min(1, Math.max(0, progress));
  const c = CONTROL[route];
  const a = (1 - s) * (1 - s);
  const b = 2 * (1 - s) * s;
  const d = s * s;
  return { x: a * FROM.x + b * c.x + d * TO.x, y: a * FROM.y + b * c.y + d * TO.y };
}

const pathD = (r: Route) => `M ${FROM.x} ${FROM.y} Q ${CONTROL[r].x} ${CONTROL[r].y} ${TO.x} ${TO.y}`;
const pad = (n: number) => String(n + 1).padStart(3, "0");

interface Props {
  t: Content["how"];
}

export function ResumeDiagram({ t }: Props) {
  const root = useRef<HTMLDivElement>(null);
  const clock = useLoopClock(LOOP, root);
  const f = frameAt(clock.t);
  const phase = t.phases[f.phase];
  const lines = Array.from({ length: Math.min(f.delivered, 6) }, (_, i) => f.delivered - Math.min(f.delivered, 6) + i);

  return (
    <div className="diagram" ref={root}>
      <div className="diagram__scroll">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${phase.label}: ${phase.caption}`}>
          {/* routes */}
          {(["wifi", "cell"] as Route[]).map((r) => {
            const active = f.route === r;
            const down = active && !f.linkUp;
            return (
              <g key={r}>
                <path
                  d={pathD(r)}
                  className={`route ${active ? "route--active" : ""} ${down ? "route--down" : ""}`}
                />
                <text x={450} y={r === "wifi" ? 82 : 248} className={`route__label ${active ? "is-active" : ""}`}>
                  {r === "wifi" ? "Wi-Fi" : "5G"}
                </text>
                {down && (
                  <text x={450} y={r === "wifi" ? 100 : 228} className="route__cut">
                    ✕
                  </text>
                )}
              </g>
            );
          })}

          {/* laptop */}
          <g>
            <rect {...LAPTOP} rx={14} className="box" />
            <text x={LAPTOP.x + 16} y={LAPTOP.y + 26} className="box__title">
              {t.laptop}
            </text>
            <rect x={LAPTOP.x + 12} y={LAPTOP.y + 38} width={LAPTOP.w - 24} height={112} rx={6} className="term" />
            {lines.map((n, i) => (
              <text key={n} x={LAPTOP.x + 22} y={LAPTOP.y + 58 + i * 17} className="term__line">
                step {pad(n)} <tspan className="term__ok">ok</tspan>
              </text>
            ))}
            <text x={LAPTOP.x + 16} y={LAPTOP.y + LAPTOP.h - 10} className="box__meta">
              {t.delivered}: {f.delivered}
            </text>
            <text x={LAPTOP.x + LAPTOP.w / 2} y={LAPTOP.y + LAPTOP.h + 26} className={`ip ${f.route === "cell" ? "ip--changed" : ""}`}>
              {IP[f.route]}
            </text>
          </g>

          {/* server */}
          <g>
            <rect {...SERVER} rx={14} className="box" />
            <text x={SERVER.x + 16} y={SERVER.y + 26} className="box__title">
              {t.server}
            </text>
            {f.queued.slice(0, 18).map((id, i) => (
              <rect
                key={id}
                x={SERVER.x + 16 + (i % 9) * 22}
                y={SERVER.y + 48 + Math.floor(i / 9) * 22}
                width={17}
                height={17}
                rx={4}
                className="queued"
              />
            ))}
            <text x={SERVER.x + 16} y={SERVER.y + LAPTOP.h - 10} className="box__meta">
              {t.buffer}: {f.unacked}
            </text>
          </g>

          {/* packets */}
          {f.packets.map((p) => {
            const { x, y } = pointOn(p.route, p.progress);
            return (
              <g key={p.key} transform={`translate(${x} ${y})`} opacity={p.opacity}>
                <circle r={13} className={`packet ${p.lost ? "packet--lost" : ""} ${p.replay ? "packet--replay" : ""}`} />
                <text className="packet__id" dy={4}>
                  {p.id + 1}
                </text>
              </g>
            );
          })}
          {f.packets.some((p) => p.lost) && (
            <text x={450} y={H / 2 + 5} className="route__lost">
              {t.lost}
            </text>
          )}
        </svg>
      </div>

      <div className="diagram__controls">
        <button type="button" className="chip chip--ghost" onClick={() => clock.setPlaying(!clock.playing)}>
          {clock.playing ? `❚❚ ${t.pause}` : `▶ ${t.play}`}
        </button>
        {PHASE_STARTS.map(([at, key]) => (
          <button
            key={key}
            type="button"
            className={`chip ${f.phase === key ? "chip--active" : ""}`}
            aria-pressed={f.phase === key}
            onClick={() => clock.seek(at + 0.01)}
          >
            {t.phases[key].label}
          </button>
        ))}
      </div>
      <p className="diagram__caption" aria-live="polite">
        <strong>{phase.label}.</strong> {phase.caption}
      </p>
    </div>
  );
}

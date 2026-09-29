import { useEffect, useRef } from "react";
import type { Content } from "../content/types";
import { useLoopClock } from "../useLoopClock";
import { chapterAt, frameAt, LOOP, PHASE_STARTS, TOTAL, type Route } from "./resumeSim";

// Geometry of the SVG, in viewBox units. Server on top, laptop below, the two
// networks as arcs either side, so the figure fits a narrow sticky column.
const W = 520;
const H = 600;
const SERVER = { x: 145, y: 10, w: 230, h: 130 };
const LAPTOP = { x: 145, y: 380, w: 230, h: 180 };
const FROM = { x: W / 2, y: SERVER.y + SERVER.h };
const TO = { x: W / 2, y: LAPTOP.y };
const MID_Y = (FROM.y + TO.y) / 2;
const CONTROL: Record<Route, { x: number; y: number }> = {
  wifi: { x: 30, y: MID_Y },
  cell: { x: W - 30, y: MID_Y },
};
/** Where each arc is furthest out, for its label and the "link down" mark. */
const APEX: Record<Route, number> = { wifi: (FROM.x + CONTROL.wifi.x) / 2, cell: (FROM.x + CONTROL.cell.x) / 2 };
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
const pad = (n: number) => String(n).padStart(2, "0");
/** SVG rect attributes for one of the boxes above. */
const rect = (b: { x: number; y: number; w: number; h: number }) => ({ x: b.x, y: b.y, width: b.w, height: b.h });
const BAR = { x: LAPTOP.x + 22, y: LAPTOP.y + 84, w: LAPTOP.w - 44, h: 10 };

interface Props {
  t: Content["how"];
}

/**
 * The resume story as a sticky figure beside four steps. The highlighted step
 * follows the animation; on wide screens, scrolling a step to the middle of
 * the viewport (or clicking it) jumps the animation there.
 */
export function ResumeDiagram({ t }: Props) {
  const root = useRef<HTMLDivElement>(null);
  const steps = useRef<(HTMLLIElement | null)[]>([]);
  const clock = useLoopClock(LOOP, root);
  const f = frameAt(clock.t);
  const chapter = chapterAt(clock.t);
  const done = f.delivered / TOTAL;
  const { seek } = clock;

  useEffect(() => {
    if (!window.matchMedia("(min-width: 960px)").matches) return;
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          const i = steps.current.indexOf(e.target as HTMLLIElement);
          if (i >= 0) seek(PHASE_STARTS[i][0] + 0.01);
        }
      },
      { rootMargin: "-50% 0px -50% 0px" },
    );
    for (const el of steps.current) if (el) io.observe(el);
    return () => io.disconnect();
  }, [seek]);

  return (
    <div className="wide scrolly" ref={root}>
      <figure className="scrolly__figure">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${t.phases[chapter].label}: ${t.phases[chapter].caption}`}>
          {/* routes */}
          {(["wifi", "cell"] as Route[]).map((r) => {
            const active = f.route === r;
            const down = active && !f.linkUp;
            return (
              <g key={r}>
                <path d={pathD(r)} className={`route ${active ? "route--active" : ""} ${down ? "route--down" : ""}`} />
                <text
                  x={r === "wifi" ? APEX.wifi - 22 : APEX.cell + 22}
                  y={MID_Y + 5}
                  className={`route__label route__label--${r} ${active ? "is-active" : ""}`}
                >
                  {r === "wifi" ? "Wi-Fi" : "5G"}
                </text>
                {down && (
                  <text x={APEX[r]} y={MID_Y + 6} className="route__cut">
                    ✕
                  </text>
                )}
              </g>
            );
          })}

          {/* server */}
          <g>
            <rect {...rect(SERVER)} rx={18} className="box" />
            <text x={SERVER.x + 18} y={SERVER.y + 28} className="box__title">
              {t.server}
            </text>
            {f.queued.slice(0, 18).map((id, i) => (
              <rect
                key={id}
                x={SERVER.x + 18 + (i % 9) * 22}
                y={SERVER.y + 46 + Math.floor(i / 9) * 22}
                width={16}
                height={16}
                rx={3}
                className="queued"
              />
            ))}
            <text x={SERVER.x + 18} y={SERVER.y + SERVER.h - 14} className="box__meta">
              {t.buffer}: {f.unacked}
            </text>
          </g>

          {/* laptop */}
          <g>
            <rect {...rect(LAPTOP)} rx={18} className="box" />
            <text x={LAPTOP.x + 18} y={LAPTOP.y + 28} className="box__title">
              {t.laptop}
            </text>
            <rect x={LAPTOP.x + 12} y={LAPTOP.y + 40} width={LAPTOP.w - 24} height={108} rx={8} className="term" />
            {/* The same long job as the recorded demo, as seen on the laptop. */}
            <text x={BAR.x} y={LAPTOP.y + 66} className="term__line">
              $ ssh devbox reindex
            </text>
            <rect {...rect(BAR)} rx={2} className="bar__track" />
            <rect x={BAR.x} y={BAR.y} width={BAR.w * done} height={BAR.h} rx={2} className="bar__fill" />
            <text x={BAR.x} y={BAR.y + 34} className="term__line">
              {Math.round(done * 100)}%{"  "}shard {pad(f.delivered)}/{TOTAL}
              {f.delivered === TOTAL && <tspan className="term__ok">{"  "}✓</tspan>}
            </text>
            <text x={LAPTOP.x + 18} y={LAPTOP.y + LAPTOP.h - 14} className="box__meta">
              {t.delivered}: {f.delivered}
            </text>
            <text x={W / 2} y={LAPTOP.y + LAPTOP.h + 28} className={`ip ${f.route === "cell" ? "ip--changed" : ""}`}>
              {IP[f.route]}
            </text>
          </g>

          {/* packets */}
          {f.packets.map((p) => {
            const { x, y } = pointOn(p.route, p.progress);
            return (
              <g key={p.key} transform={`translate(${x} ${y})`} opacity={p.opacity}>
                <circle r={12} className={`packet ${p.lost ? "packet--lost" : ""} ${p.replay ? "packet--replay" : ""}`} />
                <text className="packet__id" dy={4}>
                  {p.id + 1}
                </text>
              </g>
            );
          })}
          {f.packets.some((p) => p.lost) && (
            <text x={W / 2} y={MID_Y + 5} className="route__lost">
              {t.lost}
            </text>
          )}
        </svg>

        <div className="timeline">
          <button
            type="button"
            className="timeline__play"
            onClick={() => clock.setPlaying(!clock.playing)}
            aria-label={clock.playing ? t.pause : t.play}
          >
            {clock.playing ? "❚❚" : "▶"}
          </button>
          <div className="timeline__track" aria-hidden="true">
            {PHASE_STARTS.map(([at, key], i) => {
              const end = PHASE_STARTS[i + 1]?.[0] ?? LOOP;
              const fill = Math.min(1, Math.max(0, (clock.t - at) / (end - at)));
              return (
                <span key={key} className="timeline__seg" style={{ flexGrow: end - at }}>
                  <span className="timeline__fill" style={{ transform: `scaleX(${fill})` }} />
                </span>
              );
            })}
          </div>
        </div>
      </figure>

      <ol className="scrolly__steps">
        {PHASE_STARTS.map(([at, key], i) => (
          <li
            key={key}
            ref={(el) => {
              steps.current[i] = el;
            }}
            className={`scrolly__step ${chapter === key ? "is-active" : ""}`}
          >
            <button type="button" onClick={() => clock.seek(at + 0.01)} aria-current={chapter === key ? "step" : undefined}>
              <span className="scrolly__num">{pad(i + 1)}</span>
              <span className="scrolly__title">{t.phases[key].label}</span>
              <span className="scrolly__body">{t.phases[key].caption}</span>
            </button>
          </li>
        ))}
      </ol>
    </div>
  );
}

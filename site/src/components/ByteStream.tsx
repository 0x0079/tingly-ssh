import { useRef } from "react";
import type { Content } from "../content/types";
import { useLoopClock } from "../useLoopClock";

// The hero illustration: a grid of cells, one per chunk of output, filling in
// the order they reach the terminal. Midway the network goes away: new cells
// are held on the server (outlined), then replayed into the gap when it comes
// back, so the grid always ends complete and in order.

const COLS = 8;
const CELLS = COLS * 5;
const STEP = 0.22; // seconds per cell while sending
const REPLAY_STEP = 0.09;
const OUTAGE_FROM = 16; // first cell held during the outage
const OUTAGE_TO = 28; // first cell sent after it
const OUTAGE_AT = OUTAGE_FROM * STEP;
const BACK_AT = OUTAGE_AT + (OUTAGE_TO - OUTAGE_FROM) * STEP + 0.6;
const REPLAYED_AT = BACK_AT + (OUTAGE_TO - OUTAGE_FROM) * REPLAY_STEP;
const DONE_AT = REPLAYED_AT + (CELLS - OUTAGE_TO) * STEP;
const LOOP = DONE_AT + 2.2;

type Cell = "empty" | "sent" | "held" | "replayed";
type Status = keyof Content["hero"]["art"];

function cellAt(i: number, t: number): Cell {
  if (i < OUTAGE_FROM) return t >= i * STEP ? "sent" : "empty";
  if (i < OUTAGE_TO) {
    if (t >= BACK_AT + (i - OUTAGE_FROM) * REPLAY_STEP) return "replayed";
    return t >= i * STEP ? "held" : "empty";
  }
  return t >= REPLAYED_AT + (i - OUTAGE_TO) * STEP ? "sent" : "empty";
}

function statusAt(t: number): Status {
  if (t < OUTAGE_AT) return "sending";
  if (t < BACK_AT) return "outage";
  if (t < REPLAYED_AT) return "replay";
  return t < DONE_AT ? "sending" : "done";
}

export function ByteStream({ t }: { t: Content["hero"]["art"] }) {
  const root = useRef<HTMLDivElement>(null);
  const clock = useLoopClock(LOOP, root);
  // Without motion, show the finished story rather than an empty grid.
  const now = clock.playing ? clock.t : clock.t || DONE_AT;
  const status = statusAt(now);
  const delivered = Array.from({ length: CELLS }, (_, i) => cellAt(i, now)).filter(
    (c) => c === "sent" || c === "replayed",
  ).length;

  return (
    <div className="bytes" ref={root} aria-hidden="true">
      <div className="bytes__grid" style={{ gridTemplateColumns: `repeat(${COLS}, 1fr)` }}>
        {Array.from({ length: CELLS }, (_, i) => (
          <span key={i} className={`bytes__cell bytes__cell--${cellAt(i, now)}`} />
        ))}
      </div>
      <p className={`bytes__status bytes__status--${status}`}>
        <span className="bytes__count">
          {String(delivered).padStart(2, "0")}/{CELLS}
        </span>
        {t[status]}
      </p>
    </div>
  );
}

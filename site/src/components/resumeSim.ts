// A pure, deterministic model of the "How it works" animation. Given a time
// in the loop it says where every chunk of output is. The component only
// draws what this returns, so the story can be tuned here in one place.
//
// The story mirrors the real demo: output flows server -> laptop, the laptop
// switches from Wi-Fi to 5G (QUIC migrates, nothing is lost), then the network
// disappears (chunks in flight are lost, new output waits in the replay
// buffer), then a new link replays exactly what is missing.

export const LOOP = 21; // seconds
export const SWITCH_AT = 4.5;
export const OUTAGE_AT = 8.5;
export const LINK_BACK_AT = 14.5; // the network returns
const HANDSHAKE = 0.5; // new link + offset exchange
export const RESUME_AT = LINK_BACK_AT + HANDSHAKE;

const EMIT_EVERY = 0.6;
const TRAVEL = 1.5;
const CATCH_UP_GAP = 0.16; // spacing of replayed chunks
const LAST_EMIT = LOOP - TRAVEL - 1; // let the wire drain before looping
const LOST_FADE = 0.8;

export type PhaseKey = "normal" | "switch" | "outage" | "resume";
export type Route = "wifi" | "cell";

export const PHASE_STARTS: [number, PhaseKey][] = [
  [0, "normal"],
  [SWITCH_AT, "switch"],
  [OUTAGE_AT, "outage"],
  [LINK_BACK_AT, "resume"],
];

export function phaseAt(t: number): PhaseKey {
  if (t >= LINK_BACK_AT && t < LINK_BACK_AT + 3.5) return "resume";
  if (t >= OUTAGE_AT && t < LINK_BACK_AT) return "outage";
  if (t >= SWITCH_AT && t < SWITCH_AT + 2.5) return "switch";
  return "normal";
}

interface Flight {
  dep: number;
  route: Route;
  /** Set when the network vanished under this flight. */
  lost: boolean;
}

interface Chunk {
  id: number;
  emit: number;
  flights: Flight[];
  arrive: number;
}

function schedule(): Chunk[] {
  const chunks: Chunk[] = [];
  let lastDep = -Infinity;
  for (let id = 0; id * EMIT_EVERY <= LAST_EMIT; id++) {
    const emit = id * EMIT_EVERY;
    const flights: Flight[] = [];
    let dep = Math.max(emit, lastDep + CATCH_UP_GAP);
    if (dep >= OUTAGE_AT && dep < RESUME_AT) dep = Math.max(RESUME_AT, lastDep + CATCH_UP_GAP);
    const route = (d: number): Route => (d < SWITCH_AT ? "wifi" : "cell");
    if (dep < OUTAGE_AT && dep + TRAVEL > OUTAGE_AT) {
      // In flight when the network went away: lost, and replayed on resume.
      flights.push({ dep, route: route(dep), lost: true });
      dep = Math.max(RESUME_AT, lastDep + CATCH_UP_GAP);
    }
    flights.push({ dep, route: route(dep), lost: false });
    lastDep = dep;
    chunks.push({ id, emit, flights, arrive: dep + TRAVEL });
  }
  return chunks;
}

const CHUNKS = schedule();
/** How many chunks the story's job produces in one loop. */
export const TOTAL = CHUNKS.length;

export interface Packet {
  key: string;
  id: number;
  route: Route;
  /** 0 at the server, 1 at the laptop. */
  progress: number;
  lost: boolean;
  /** 1 while visible, fading to 0 for lost packets. */
  opacity: number;
  replay: boolean;
}

export interface Frame {
  phase: PhaseKey;
  route: Route;
  linkUp: boolean;
  packets: Packet[];
  /** Chunks waiting on the server, not yet sent. */
  queued: number[];
  /** Everything the server still holds: queued, plus sent but unacknowledged. */
  unacked: number;
  delivered: number;
}

export function frameAt(t: number): Frame {
  const packets: Packet[] = [];
  const queued: number[] = [];
  let unacked = 0;
  let delivered = 0;

  for (const c of CHUNKS) {
    if (t < c.emit) break;
    if (t >= c.arrive) {
      delivered++;
      continue;
    }
    unacked++;
    const live = c.flights[c.flights.length - 1];
    if (t < live.dep) {
      // A lost flight may still be fading out while the chunk waits for replay.
      const lostFlight = c.flights.find((f) => f.lost);
      if (lostFlight && t >= lostFlight.dep && t < OUTAGE_AT + LOST_FADE) {
        const frozen = Math.min(t, OUTAGE_AT);
        packets.push({
          key: `${c.id}-lost`,
          id: c.id,
          route: lostFlight.route,
          progress: (frozen - lostFlight.dep) / TRAVEL,
          lost: t >= OUTAGE_AT,
          opacity: t < OUTAGE_AT ? 1 : 1 - (t - OUTAGE_AT) / LOST_FADE,
          replay: false,
        });
      }
      if (!lostFlight || t >= OUTAGE_AT) queued.push(c.id);
      continue;
    }
    packets.push({
      key: `${c.id}-${live.dep}`,
      id: c.id,
      route: live.route,
      progress: (t - live.dep) / TRAVEL,
      lost: false,
      opacity: 1,
      replay: live.dep >= RESUME_AT && live.dep > c.emit + 0.01,
    });
  }

  return {
    phase: phaseAt(t),
    route: t < SWITCH_AT ? "wifi" : "cell",
    linkUp: t < OUTAGE_AT || t >= LINK_BACK_AT,
    packets,
    queued,
    unacked,
    delivered,
  };
}

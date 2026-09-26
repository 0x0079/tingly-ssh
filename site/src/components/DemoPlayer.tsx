import { useEffect, useRef, useState } from "react";
import * as AsciinemaPlayer from "asciinema-player";
import "asciinema-player/dist/bundle/asciinema-player.css";
import markersJson from "../assets/demo-markers.json";
import type { Content } from "../content/types";

// demo-markers.json is written by demo/record.sh next to the recording:
// [[seconds, "switch" | "outage" | "back"], ...]
type ChapterKey = keyof Content["demo"]["chapters"];
const markers = markersJson as [number, Exclude<ChapterKey, "start">][];
const CHAPTERS: [number, ChapterKey][] = [[0, "start"], ...markers];

interface Props {
  t: Content["demo"];
}

/**
 * Replays the real recording (public/demo.cast) with asciinema-player.
 * It starts when scrolled into view and offers chapter buttons.
 */
export function DemoPlayer({ t }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const player = useRef<AsciinemaPlayer.Player | null>(null);
  const [chapter, setChapter] = useState<ChapterKey>("start");

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const p = AsciinemaPlayer.create(`${import.meta.env.BASE_URL}demo.cast`, el, {
      preload: true,
      poster: "npt:0:12",
      loop: true,
      fit: "width",
      theme: "tingly",
      terminalFontSize: "small",
      markers: markers.map(([time, key]) => [time, key]),
      controls: true,
    });
    player.current = p;
    p.addEventListener("marker", ({ label }) => setChapter(label as ChapterKey));
    p.addEventListener("ended", () => setChapter("start"));

    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting && !reduced) {
          void p.play();
          io.disconnect();
        }
      },
      { threshold: 0.4 },
    );
    io.observe(el);

    return () => {
      io.disconnect();
      p.dispose();
      player.current = null;
    };
  }, []);

  function jump(time: number, key: ChapterKey) {
    const p = player.current;
    if (!p) return;
    setChapter(key);
    // Land slightly before the event so its caption is visible as it happens.
    void p.seek(Math.max(0, time - 1)).then(() => p.play());
  }

  return (
    <div className="demo">
      <div className="demo__labels" aria-hidden="true">
        <span className="demo__label demo__label--bad">{t.left}</span>
        <span className="demo__label demo__label--good">{t.right}</span>
      </div>
      <div className="demo__frame" ref={host} />
      <div className="demo__chapters" role="group" aria-label="Chapters">
        {CHAPTERS.map(([time, key]) => (
          <button
            key={key}
            type="button"
            className={`chip ${chapter === key ? "chip--active" : ""}`}
            aria-pressed={chapter === key}
            onClick={() => jump(time, key)}
          >
            {t.chapters[key]}
          </button>
        ))}
      </div>
    </div>
  );
}

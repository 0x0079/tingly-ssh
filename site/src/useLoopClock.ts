import { useCallback, useEffect, useRef, useState, type RefObject } from "react";

/**
 * A looping animation clock in seconds. It runs only while `target` is on
 * screen and playing, and starts paused when the user prefers reduced motion.
 */
export function useLoopClock(length: number, target: RefObject<Element | null>) {
  const reduced =
    typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const [t, setT] = useState(0);
  const [playing, setPlaying] = useState(!reduced);
  const [visible, setVisible] = useState(false);
  const tRef = useRef(0);

  useEffect(() => {
    const el = target.current;
    if (!el) return;
    const io = new IntersectionObserver(([e]) => setVisible(e.isIntersecting), { threshold: 0.2 });
    io.observe(el);
    return () => io.disconnect();
  }, [target]);

  useEffect(() => {
    if (!playing || !visible) return;
    let raf = 0;
    let last = performance.now();
    const tick = (now: number) => {
      tRef.current = (tRef.current + (now - last) / 1000) % length;
      last = now;
      setT(tRef.current);
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing, visible, length]);

  const seek = useCallback((to: number) => {
    tRef.current = to;
    setT(to);
  }, []);

  return { t, playing, setPlaying, seek };
}

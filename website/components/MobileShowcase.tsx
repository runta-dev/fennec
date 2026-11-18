"use client";
// The iPhone app on its own: it replays the walkthrough's three sessions on its
// own clock. Playback follows the same rules as the desktop walkthrough: it
// runs while visible, pauses for eight seconds after you tap, and Reduce
// Motion shows the finished list.
import { useEffect, useRef, useState } from "react";
import MobileApp from "./MobileApp";
import { tasks } from "./demo-scenario";
import { autoChat } from "./mobile-scenario";

// The last session finishes, then the list rests before the loop restarts.
const end = tasks[tasks.length - 1].done + 4000;

export default function MobileShowcase() {
  const root = useRef<HTMLDivElement>(null);
  const resumeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [time, setTime] = useState(0), [playing, setPlaying] = useState(true);
  const [visible, setVisible] = useState(false), [hidden, setHidden] = useState(false);
  const [override, setOverride] = useState<number | "home" | null>(null);
  const reducedMotion = useRef(false);
  useEffect(() => {
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    const onMotion = () => {
      reducedMotion.current = motion.matches;
      if (motion.matches) { clearTimeout(resumeTimer.current); setPlaying(false); setTime(end); setOverride(null); }
      else setPlaying(true);
    };
    onMotion();
    const visibility = () => setHidden(document.hidden);
    visibility();
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), { threshold: 0.2 });
    observer.observe(root.current!);
    motion.addEventListener("change", onMotion); document.addEventListener("visibilitychange", visibility);
    return () => { clearTimeout(resumeTimer.current); observer.disconnect(); motion.removeEventListener("change", onMotion); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => {
    if (!playing || !visible || hidden) return;
    let frame = 0, last = performance.now();
    const tick = (now: number) => {
      if (now - last >= 45) {
        const elapsed = Math.min(now - last, 150); last = now;
        setTime(value => value + elapsed >= end ? 0 : value + elapsed);
      }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick); return () => cancelAnimationFrame(frame);
  }, [playing, visible, hidden]);
  const interact = (target: number | "home") => {
    setOverride(target); setPlaying(false);
    clearTimeout(resumeTimer.current);
    if (reducedMotion.current) return;
    resumeTimer.current = setTimeout(() => { setOverride(null); setPlaying(true); }, 8000);
  };
  const target = override ?? autoChat(time);
  const chat = typeof target === "number" && time >= tasks[target].start + 1500 ? target : null;
  const tapping = override === null ? tasks.findIndex(task => time >= task.start + 2100 && time < task.start + 2500) : -1;
  return <div className="mobile-showcase" ref={root} data-playing={playing && visible && !hidden}>
    <MobileApp time={time} stopped={[]} chat={chat} tapping={tapping < 0 ? null : tapping} onOpen={index => interact(index)} onBack={() => interact("home")}/>
  </div>;
}

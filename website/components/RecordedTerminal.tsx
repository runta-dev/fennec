"use client";
import { useEffect, useRef, useState } from "react";

type Recording = { cols: number; rows: number; duration: number; chunks: { t: number; b: string }[] };
type TerminalInstance = {
  open: (element: HTMLElement) => void; dispose: () => void; reset: () => void;
  write: (data: Uint8Array, callback?: () => void) => void;
  loadAddon: (addon: { dispose: () => void }) => void;
  element?: HTMLElement;
};
declare global { interface Window { Terminal: new (options: Record<string, unknown>) => TerminalInstance } }
let library: Promise<void> | undefined;
function loadTerminal() {
  return library ??= new Promise<void>((resolve, reject) => {
    const style = document.createElement("link");
    style.rel = "stylesheet"; style.href = "/demo-native/vendor/xterm.css"; document.head.appendChild(style);
    const script = document.createElement("script");
    script.src = "/demo-native/vendor/xterm.js";
    script.onload = () => resolve(); script.onerror = () => { library = undefined; reject(new Error("Terminal unavailable")); };
    document.head.appendChild(script);
  });
}
const recordings = new Map<string, Promise<Recording>>();
function loadRecording(name: string) {
  if (!recordings.has(name)) recordings.set(name, fetch(`/demo-recordings/${name}.json`).then(r => { if (!r.ok) throw new Error("Recording unavailable"); return r.json(); }));
  return recordings.get(name)!;
}
export default function RecordedTerminal({ name, progress }: { name: string; progress: number }) {
  const host = useRef<HTMLDivElement>(null), viewport = useRef<HTMLDivElement>(null);
  const desired = useRef(progress); desired.current = progress;
  const playback = useRef<(() => void) | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let cancelled = false, terminal: TerminalInstance | undefined, observer: ResizeObserver | undefined;
    setFailed(false);
    Promise.all([loadTerminal(), loadRecording(name), import("@xterm/addon-canvas")]).then(([, recording, { CanvasAddon }]) => {
      if (cancelled) return;
      terminal = new window.Terminal({ cols: recording.cols, rows: recording.rows, fontSize: 14, lineHeight: 1, customGlyphs: true, fontFamily: '"SFMono-Regular", Menlo, Consolas, "Liberation Mono", monospace', allowTransparency: false, disableStdin: true, scrollback: 1000, cursorBlink: false, theme: { background: "#17181b", foreground: "#d9d9df", cursor: "#c5c5ce" } });
      terminal.open(host.current!);
      // Block/box characters must fill terminal cells, independent of font
      // ascenders and fallback glyph metrics (the DOM renderer cannot do this).
      terminal.loadAddon(new CanvasAddon());
      const bytes = recording.chunks.map(c => Uint8Array.from(atob(c.b), ch => ch.charCodeAt(0)));
      let next = 0, last = -1, busy = false;
      const resize = () => {
        if (!host.current || !viewport.current) return;
        const screen = host.current.querySelector<HTMLElement>(".xterm-screen");
        if (!screen || !screen.offsetWidth) return;
        const outer = viewport.current.parentElement!;
        const style = getComputedStyle(outer);
        const availableHeight = outer.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom);
        const scale = Math.min(viewport.current.clientWidth / screen.offsetWidth, availableHeight / screen.offsetHeight);
        host.current.style.transform = `scale(${scale})`;
        const height = screen.offsetHeight * scale + "px";
        if (viewport.current.style.height !== height) viewport.current.style.height = height;
      };
      observer = new ResizeObserver(() => requestAnimationFrame(resize)); observer.observe(viewport.current!.parentElement!); observer.observe(host.current!);
      playback.current = () => {
        if (busy || cancelled || !terminal) return;
        const time = Math.min(1, Math.max(0, desired.current)) * recording.duration;
        if (time < last) { terminal.reset(); next = 0; }
        last = time;
        const pending: Uint8Array[] = [];
        while (next < recording.chunks.length && recording.chunks[next].t <= time) pending.push(bytes[next++]);
        if (!pending.length) return;
        const joined = new Uint8Array(pending.reduce((n, b) => n + b.length, 0));
        let offset = 0; for (const b of pending) { joined.set(b, offset); offset += b.length; }
        busy = true;
        terminal.write(joined, () => { busy = false; resize(); playback.current?.(); });
      };
      playback.current();
    }).catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; playback.current = null; observer?.disconnect(); terminal?.dispose(); };
  }, [name]);
  useEffect(() => { playback.current?.(); }, [progress]);
  // Recorded mouse modes must not capture page scrolling or keyboard focus.
  return <div className="recorded-terminal" role="img" aria-label={`Recorded ${name} terminal session`}><div className="recorded-viewport" ref={viewport}><div className="recorded-screen" ref={host} inert /></div>{failed && <p className="recording-error">Terminal recording could not load.</p>}</div>;
}

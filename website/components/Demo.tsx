"use client";
import { useEffect, useRef, useState } from "react";
import { ChevronDown, Monitor, Plus, PanelLeft, Terminal, X } from "lucide-react";
import FennecPanel from "./FennecPanel";
import RecordedTerminal from "./RecordedTerminal";
import { duration, tasks, sessionState, sessionTitle, type PanelTab } from "./demo-scenario";
import "./demo.css";

export default function Demo() {
  const root = useRef<HTMLElement>(null);
  const [time, setTime] = useState(0), [playing, setPlaying] = useState(true);
  const [visible, setVisible] = useState(false), [hidden, setHidden] = useState(false);
  const [panelOverride, setPanelOverride] = useState<PanelTab | null>(null);
  const [tabOverride, setTabOverride] = useState<number | null>(null);
  const [stopped, setStopped] = useState<number[]>([]);
  useEffect(() => {
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    const onMotion = () => { if (motion.matches) { setPlaying(false); setTime(duration); } };
    onMotion();
    const visibility = () => setHidden(document.hidden);
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), { threshold: 0.2 });
    if (root.current) observer.observe(root.current);
    motion.addEventListener("change", onMotion); document.addEventListener("visibilitychange", visibility);
    return () => { observer.disconnect(); motion.removeEventListener("change", onMotion); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => {
    if (!playing || !visible || hidden) return;
    let frame = 0, last = performance.now();
    const tick = (now: number) => {
      if (now - last >= 45) { const elapsed = Math.min(now - last, 150); last = now; setTime(value => value + elapsed >= duration + 4000 ? 0 : value + elapsed); }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick); return () => cancelAnimationFrame(frame);
  }, [playing, visible, hidden]);
  const current = Math.min(3, Math.floor(time / 10000)), active = tabOverride ?? current;
  const panel: PanelTab = panelOverride ?? (time < 1500 ? "Agents" : time >= 35000 && time < 39000 ? "Governance" : time >= 39000 && time < 43000 ? "Account" : "Sessions");
  const sessions = tasks.map((task, index) => ({ task, index })).filter(({ task, index }) => time >= task.start + 1500 && !stopped.includes(index));
  const pause = () => setPlaying(false);
  function attach(index: number) { pause(); setTabOverride(index); setPanelOverride("Sessions"); }
  const task = tasks[Math.min(active, 2)], age = time - active * 10000, shell = age < 1200;
  return <section id="demo" ref={root} className="workflow-demo" aria-label="Fennec terminal workflow" data-playing={playing && visible && !hidden} tabIndex={0} onKeyDown={event => {
    if (event.target !== event.currentTarget) return;
    if (event.key === " ") { event.preventDefault(); setPanelOverride(null); setTabOverride(null); setPlaying(value => !value); }
    if (event.key === "Escape") pause();
  }}>
    <div className="workflow-desktop">
      <div className="fennec-window"><FennecPanel time={time} tab={panel} onTab={value => { pause(); setPanelOverride(value); }} onAttach={attach} onInteract={pause} stopped={stopped} onStop={index => { pause(); setStopped(value => [...value, index]); }} /></div>
      <div className="herdr-window">
        <aside className="herdr-sidebar">
          <div className="herdr-brand"><div className="mac-dots" aria-hidden="true"><i/><i/><i/></div><PanelLeft size={15}/></div>
          <div className="herdr-sidebar-heading">Spaces <Plus size={13}/></div>
          <div className="herdr-computer"><Monitor size={14}/>This computer<ChevronDown size={12}/></div>
          <div className="herdr-project"><span>R</span>relay</div>
          <div className="herdr-sidebar-heading herdr-agents-heading">Agents <span>{sessions.length}</span></div>
          {["Working", "Idle"].map(state => {
            const group = sessions.filter(({ task }) => sessionState(task, time) === state);
            return group.length > 0 && <div className="herdr-group" key={state}><small>{state} <span>{group.length}</span></small>{group.map(({ task, index }) => <button key={task.command} className={active === index ? "selected" : ""} onClick={() => attach(index)} title={sessionTitle(task, time)}><img src={"/icons/" + task.icon} alt=""/><span>{sessionTitle(task, time)}</span><span className={"herdr-state " + state.toLowerCase()}/></button>)}</div>;
          })}
        </aside>
        <div className="herdr-main">
          <div className="herdr-tabs" role="tablist" aria-label="Terminal tabs">
            {Array.from({ length: current + 1 }, (_, index) => <button role="tab" aria-selected={active === index} aria-controls="demo-terminal" id={"terminal-tab-" + index} key={index} onClick={() => { pause(); setTabOverride(index); }}>{index < 3 && time >= tasks[index].start + 1500 ? <img src={"/icons/" + tasks[index].icon} alt=""/> : <Terminal size={14}/>}<span>{index + 1}</span><X size={12} aria-hidden="true"/></button>)}
            <button className="herdr-new-tab" aria-label="Open next terminal tab" disabled={current === 3} onClick={() => { setTime((current + 1) * 10000); setTabOverride(null); setPanelOverride(null); setPlaying(true); }}><Plus size={17}/></button>
          </div>
          <div className="herdr-terminal" id="demo-terminal" role="tabpanel" aria-labelledby={"terminal-tab-" + active}>
            <div className="herdr-terminal-heading">{shell || active === 3 ? <Terminal size={15}/> : <img src={"/icons/" + task.icon} alt=""/>}<strong>{shell ? "zsh" : active === 3 ? "fen" : task.name}</strong><span>~/repos/relay</span></div>
            <RecordedTerminal name={active === 3 ? "fen" : task.command} progress={age / (active === 3 ? 5000 : task.done - task.start)} />
          </div>
        </div>
      </div>
    </div>
  </section>;
}

"use client";
import { useEffect, useRef, useState } from "react";
import { ChevronDown, Monitor, Plus, PanelLeft, Terminal, X } from "lucide-react";
import FennecPanel from "./FennecPanel";
import RecordedTerminal from "./RecordedTerminal";
import MobileApp from "./MobileApp";
import { autoChat } from "./mobile-scenario";
import { duration, pickerStart, pickerDuration, tasks, sessionState, sessionTitle, type Appearance, type PanelTab } from "./demo-scenario";
import "./demo.css";

export default function Demo() {
  const root = useRef<HTMLElement>(null);
  const resumeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const reducedMotion = useRef(false), userPaused = useRef(false);
  const [cycle, setCycle] = useState(0);
  const [time, setTime] = useState(0), [playing, setPlaying] = useState(true);
  const [visible, setVisible] = useState(false), [hidden, setHidden] = useState(false);
  const [panelOverride, setPanelOverride] = useState<PanelTab | null>(null);
  const [tabOverride, setTabOverride] = useState<number | null>(null);
  const [stopped, setStopped] = useState<number[]>([]);
  const [phoneOverride, setPhoneOverride] = useState<number | "home" | null>(null);
  // The phone follows the system until the panel's Appearance is changed.
  const [appearance, setAppearance] = useState<Appearance>("system");
  useEffect(() => {
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    const onMotion = () => {
      reducedMotion.current = motion.matches;
      if (motion.matches) { clearTimeout(resumeTimer.current); setPlaying(false); setTime(duration); }
      else if (!userPaused.current) setPlaying(true);
    };
    onMotion();
    const visibility = () => setHidden(document.hidden);
    visibility();
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), { threshold: 0.2 });
    if (root.current) observer.observe(root.current);
    motion.addEventListener("change", onMotion); document.addEventListener("visibilitychange", visibility);
    return () => { clearTimeout(resumeTimer.current); observer.disconnect(); motion.removeEventListener("change", onMotion); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => {
    if (!playing || !visible || hidden) return;
    let frame = 0, last = performance.now();
    const tick = (now: number) => {
      if (now - last >= 45) { const elapsed = Math.min(now - last, 150); last = now; setTime(value => Math.min(duration + 2000, value + elapsed)); }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick); return () => cancelAnimationFrame(frame);
  }, [playing, visible, hidden]);
  useEffect(() => {
    if (time < duration + 2000) return;
    setTime(0);
    setPanelOverride(null);
    setTabOverride(null);
    setStopped([]);
    setPhoneOverride(null);
    setAppearance("system");
    // Remount the isolated native document to reset its own fixture state too.
    setCycle(value => value + 1);
  }, [time]);
  const current = time >= pickerStart ? 3 : tasks.filter(task => time >= task.start).length - 1, active = tabOverride ?? current;
  const panel: PanelTab = panelOverride ?? (time < 1500 ? "Agents" : time >= pickerStart + pickerDuration && time < pickerStart + pickerDuration + 4000 ? "Governance" : time >= pickerStart + pickerDuration + 4000 && time < duration - 1000 ? "Account" : "Sessions");
  const sessions = tasks.map((task, index) => ({ task, index })).filter(({ task, index }) => time >= task.start + 1500 && !stopped.includes(index));
  const pause = () => {
    setPlaying(false);
    clearTimeout(resumeTimer.current);
    if (userPaused.current || reducedMotion.current) return;
    resumeTimer.current = setTimeout(() => {
      setPanelOverride(null);
      setTabOverride(null);
      setPhoneOverride(null);
      setPlaying(true);
    }, 8000);
  };
  function attach(index: number) { pause(); setTabOverride(index); setPanelOverride("Sessions"); setPhoneOverride(index); }
  // The phone follows the attached session; a session that has not started or
  // was stopped leaves it on the list.
  const phoneTarget = phoneOverride ?? tabOverride ?? autoChat(time);
  const phoneChat = typeof phoneTarget === "number" && phoneTarget < 3 && time >= tasks[phoneTarget].start + 1500 && !stopped.includes(phoneTarget) ? phoneTarget : null;
  const tapping = phoneOverride === null && tabOverride === null ? tasks.findIndex(task => time >= task.start + 2100 && time < task.start + 2500) : -1;
  const task = tasks[Math.min(active, 2)], age = time - (active === 3 ? pickerStart : task.start), shell = age < 1200;
  return <section id="demo" ref={root} className="workflow-demo" aria-label="Fennec terminal workflow" data-playing={playing && visible && !hidden} data-cycle={cycle} tabIndex={0} onKeyDown={event => {
    if (event.target !== event.currentTarget) return;
    if (event.key === " ") { event.preventDefault(); clearTimeout(resumeTimer.current); userPaused.current = playing; setPanelOverride(null); setTabOverride(null); setPhoneOverride(null); setPlaying(!playing); }
    if (event.key === "Escape") { userPaused.current = true; pause(); }
  }}>
    <div className="workflow-desktop">
      <div className="fennec-window"><FennecPanel key={cycle} time={time} tab={panel} onTab={value => { pause(); setPanelOverride(value); }} onAttach={attach} onInteract={pause} onTheme={setAppearance} stopped={stopped} onStop={index => { pause(); setStopped(value => [...value, index]); }} /></div>
      <div className="phone-window" aria-label="Fennec for iPhone" role="group"><MobileApp appearance={appearance} time={time} stopped={stopped} chat={phoneChat} tapping={tapping < 0 ? null : tapping} onOpen={attach} onBack={() => { pause(); setPhoneOverride("home"); }}/></div>
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
            {Array.from({ length: current + 1 }, (_, index) => <button role="tab" aria-selected={active === index} aria-controls="demo-terminal" id={"terminal-tab-" + index} key={index} onClick={() => { pause(); setTabOverride(index); setPhoneOverride(null); }}>{index < 3 && time >= tasks[index].start + 1500 ? <img src={"/icons/" + tasks[index].icon} alt=""/> : <Terminal size={14}/>}<span>{index + 1}</span><X size={12} aria-hidden="true"/></button>)}
            <button className="herdr-new-tab" aria-label="Open next terminal tab" disabled={current === 3} onClick={() => { clearTimeout(resumeTimer.current); userPaused.current = false; setTime(current === 2 ? pickerStart : tasks[current + 1].start); setTabOverride(null); setPanelOverride(null); setPhoneOverride(null); setPlaying(true); }}><Plus size={17}/></button>
          </div>
          <div className="herdr-terminal" id="demo-terminal" role="tabpanel" aria-labelledby={"terminal-tab-" + active}>
            <div className="herdr-terminal-heading">{shell || active === 3 ? <Terminal size={15}/> : <img src={"/icons/" + task.icon} alt=""/>}<strong>{shell ? "zsh" : active === 3 ? "fen" : task.name}</strong><span>/tmp/relay-mac-demo</span></div>
            <RecordedTerminal name={active === 3 ? "fen" : task.command} progress={age / (active === 3 ? pickerDuration : task.done - task.start)} />
          </div>
        </div>
      </div>
    </div>
  </section>;
}

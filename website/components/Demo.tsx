"use client";
import { useEffect, useRef, useState } from "react";
import { ArrowRight, Check, Cloud, Laptop, Moon, Pause, Play, RotateCcw, Terminal } from "lucide-react";
import ProductTour from "./ProductTour";
import "./story.css";

const stages = [
  { label: "Start a task", short: "Start", title: "Start in your terminal.", description: "Choose Cloud computer, launch Codex, and prepare the project in the cloud.", mac: "Connected", cloud: "Working", activity: "Fixing duplicate webhook deliveries", command: "codex" },
  { label: "Step away", short: "Step away", title: "Close your Mac. Work continues.", description: "With the files and dependencies in the cloud, Codex can keep working while your Mac is offline.", mac: "Offline", cloud: "Working", activity: "Running the regression tests", command: "" },
  { label: "Come back", short: "Come back", title: "Reconnect to the same session.", description: "Open your terminal and attach. The task, its history, and its results are still there.", mac: "Reconnected", cloud: "Ready for review", activity: "The fix and test results are ready", command: "fen attach maple-river" },
];

export default function Demo() {
  const root = useRef<HTMLElement>(null);
  const [stage, setStage] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [visible, setVisible] = useState(false);
  const [hidden, setHidden] = useState(false);
  const [tourOpen, setTourOpen] = useState(false);
  useEffect(() => {
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    const onMotion = () => setPlaying(!motion.matches);
    onMotion();
    const visibility = () => setHidden(document.hidden);
    visibility();
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), { threshold: 0.35 });
    if (root.current) observer.observe(root.current);
    motion.addEventListener("change", onMotion);
    document.addEventListener("visibilitychange", visibility);
    return () => { observer.disconnect(); motion.removeEventListener("change", onMotion); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => {
    if (!playing || !visible || hidden) return;
    const timer = setTimeout(() => {
      if (stage === stages.length - 1) setPlaying(false);
      else setStage(stage + 1);
    }, 5000);
    return () => clearTimeout(timer);
  }, [stage, playing, visible, hidden]);
  const select = (index: number) => { setStage(index); setPlaying(false); };
  const toggle = () => {
    if (!playing && stage === stages.length - 1) setStage(0);
    setPlaying(!playing);
  };
  const scene = stages[stage];
  return <>
    <section id="demo" ref={root} className="session-story" aria-labelledby="story-title" data-stage={stage}>
      <div className="story-controls">
        <div className="story-tabs" role="tablist" aria-label="Session walkthrough">
          {stages.map((item, index) => <button key={item.label} id={"story-tab-" + index} role="tab" aria-selected={stage === index} aria-controls="story-scene" tabIndex={stage === index ? 0 : -1} onClick={() => select(index)} onKeyDown={event => {
            let next = index;
            if (event.key === "ArrowRight") next = (index + 1) % stages.length;
            else if (event.key === "ArrowLeft") next = (index + stages.length - 1) % stages.length;
            else if (event.key === "Home") next = 0;
            else if (event.key === "End") next = stages.length - 1;
            else return;
            event.preventDefault(); select(next); document.getElementById("story-tab-" + next)?.focus();
          }}><span className="story-step-number">0{index + 1}</span><span className="story-step-long">{item.label}</span><span className="story-step-short">{item.short}</span></button>)}
        </div>
        <button className="story-play" onClick={toggle} aria-label={playing ? "Pause walkthrough" : stage === 2 ? "Replay walkthrough" : "Play walkthrough"}>
          {playing ? <Pause size={15}/> : stage === 2 ? <RotateCcw size={15}/> : <Play size={15}/>}<span>{playing ? "Pause" : stage === 2 ? "Replay" : "Play"}</span>
        </button>
      </div>
      <div id="story-scene" role="tabpanel" aria-labelledby={"story-tab-" + stage} tabIndex={0}>
        <div className="story-heading" aria-live={playing ? "off" : "polite"}>
          <h2 id="story-title">{scene.title}</h2><p>{scene.description}</p>
        </div>
        <div className="story-computers">
          <div className={"story-mac" + (stage === 1 ? " is-offline" : "")}>
            <div className="story-machine-label"><Laptop size={17}/><strong>Your Mac</strong><span>{scene.mac}</span></div>
            <div className="story-mac-screen">
              {stage === 1 ? <div className="story-away"><Moon size={29} strokeWidth={1.4}/><strong>You’re away.</strong><span>The cloud session stays.</span></div> : <div className="story-terminal">
                <div className="story-terminal-label"><Terminal size={14}/>Your terminal</div>
                <code><span>›</span> {scene.command}</code>
                <div className="story-terminal-response">{stage === 0 ? "Cloud session started" : "Attached to your running session"}<strong>maple-river</strong></div>
                <div className="story-terminal-task">{stage === 0 ? "Fix duplicate webhook deliveries and add a regression test." : "Review the fix. Continue the conversation."}</div>
              </div>}
            </div>
          </div>
          <div className="story-connection" aria-hidden="true"><span/><ArrowRight size={18}/><span/></div>
          <div className="story-cloud">
            <div className="story-machine-label"><Cloud size={18}/><strong>Your cloud computer</strong><span className="story-online"><i/>Running</span></div>
            <div className="story-session">
              <div className="story-session-heading"><img src="/icons/codex-color.svg" width="26" height="26" alt=""/><div><strong>Codex</strong><span>Session · maple-river</span></div><span className="story-session-state">{scene.cloud}</span></div>
              <h3>Fix duplicate webhook deliveries</h3>
              <ul className="story-progress">
                <li><Check size={15}/><span>Project files and dependencies ready</span></li>
                <li>{stage > 0 ? <Check size={15}/> : <span className="story-working-dot"/>}<span>Fix the duplicate delivery</span></li>
                <li className={stage === 0 ? "is-pending" : ""}>{stage === 2 ? <Check size={15}/> : stage === 1 ? <span className="story-working-dot"/> : <span className="story-pending-dot"/>}<span>Run the regression tests</span></li>
              </ul>
              <div className="story-activity">{stage === 2 ? <Check size={14}/> : <span className="story-working-dot"/>}{scene.activity}</div>
            </div>
          </div>
        </div>
      </div>
      <div className="story-foot"><span>Illustrative walkthrough · sample session</span><p>Cloud work continues while its computer is running. Steps that need your Mac wait until it reconnects.</p></div>
    </section>
    <details className="app-tour" onToggle={event => setTourOpen(event.currentTarget.open)}>
      <summary><span>Explore the app and terminal recordings</span><span className="app-tour-plus" aria-hidden="true">+</span></summary>
      {tourOpen && <div className="app-tour-content"><p>Explore the Fennec panel with sample data, alongside recorded agent sessions. These recordings show cloud-only coding tasks. No commands run on your computer. Focus the preview and press Space to pause.</p><ProductTour/></div>}
    </details>
  </>;
}

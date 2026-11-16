"use client";
import { useEffect, useRef, useState } from "react";
import {
  ChevronDown,
  Monitor,
  Pause,
  Play,
  Plus,
  RotateCcw,
  PanelLeft,
  Terminal,
  X,
} from "lucide-react";
import FennecPanel from "./FennecPanel";
import {
  chapters,
  duration,
  tasks,
  typed,
  sessionState,
  sessionTitle,
  type PanelTab,
} from "./demo-scenario";
import "./demo.css";

const clock = (time: number) =>
  "0:" + String(Math.floor(time / 1000)).padStart(2, "0");
export default function Demo() {
  const root = useRef<HTMLElement>(null);
  const [time, setTime] = useState(0),
    [playing, setPlaying] = useState(false);
  const [visible, setVisible] = useState(false),
    [hidden, setHidden] = useState(false),
    [reduced, setReduced] = useState(false);
  const [panelOverride, setPanelOverride] = useState<PanelTab | null>(null);
  const [tabOverride, setTabOverride] = useState<number | null>(null);
  const [selected, setSelected] = useState(0),
    [stopped, setStopped] = useState<number[]>([]),
    [replay, setReplay] = useState(0);
  const [pickerMode, setPickerMode] = useState<"list" | "confirm" | "closed">(
    "list",
  );
  const started = useRef(false);
  useEffect(() => {
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    setReduced(motion.matches);
    const onMotion = () => {
      setReduced(motion.matches);
      if (motion.matches) setPlaying(false);
    };
    const onVisibility = () => setHidden(document.hidden);
    const observer = new IntersectionObserver(
      ([entry]) => {
        setVisible(entry.isIntersecting);
        if (entry.isIntersecting && !started.current) {
          started.current = true;
          if (!motion.matches) setPlaying(true);
        }
      },
      { threshold: 0.2 },
    );
    if (root.current) observer.observe(root.current);
    motion.addEventListener("change", onMotion);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      observer.disconnect();
      motion.removeEventListener("change", onMotion);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);
  useEffect(() => {
    if (!playing || !visible || hidden) return;
    let frame = 0,
      last = performance.now();
    const tick = (now: number) => {
      if (now - last >= 45) {
        const elapsed = Math.min(now - last, 150);
        last = now;
        setTime((value) => Math.min(duration, value + elapsed));
      }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [playing, visible, hidden]);
  useEffect(() => {
    if (time >= duration) setPlaying(false);
  }, [time]);
  const current = Math.min(3, Math.floor(time / 10000)),
    active = tabOverride ?? current;
  const panel: PanelTab =
    panelOverride ??
    (time < 1500
      ? "Agents"
      : time >= 35000 && time < 39000
        ? "Governance"
        : time >= 39000 && time < 43000
          ? "Account"
          : "Sessions");
  const sessions = tasks
    .map((task, index) => ({ task, index }))
    .filter(
      ({ task, index }) =>
        time >= task.start + 1500 && !stopped.includes(index),
    );
  const pause = () => setPlaying(false);
  function seek(value: number, play = false) {
    setTime(value);
    setPlaying(play);
    setPanelOverride(null);
    setTabOverride(null);
    setStopped([]);
    setSelected(0);
    setPickerMode("list");
  }
  function restart() {
    setReplay((value) => value + 1);
    seek(0, true);
  }
  function attach(index: number) {
    pause();
    setTabOverride(index);
    setPanelOverride("Sessions");
  }
  function togglePlay() {
    if (time >= duration) {
      restart();
      return;
    }
    setPlaying(!playing);
    setPanelOverride(null);
    setTabOverride(null);
  }
  function pickerKey(event: React.KeyboardEvent) {
    if (pickerMode === "confirm") {
      if (["Enter", "y", "Escape", "n"].includes(event.key)) {
        event.preventDefault();
        if (["Enter", "y"].includes(event.key)) stopSelected();
        else setPickerMode("list");
      }
      return;
    }
    if (["x", "r", "Escape"].includes(event.key)) {
      event.preventDefault();
      pause();
      if (event.key === "x" && sessions.length) setPickerMode("confirm");
      if (event.key === "r") setSelected(0);
      if (event.key === "Escape") setPickerMode("closed");
      return;
    }
    if (
      sessions.length &&
      ["ArrowDown", "j", "ArrowUp", "k", "Enter"].includes(event.key)
    ) {
      event.preventDefault();
      pause();
      if (event.key === "Enter")
        attach(sessions[Math.min(selected, sessions.length - 1)].index);
      else
        setSelected(
          (value) =>
            (value +
              (["ArrowDown", "j"].includes(event.key) ? 1 : -1) +
              sessions.length) %
            sessions.length,
        );
    }
  }
  function stopSelected() {
    const session = sessions[Math.min(selected, sessions.length - 1)];
    if (session) setStopped((value) => [...value, session.index]);
    setSelected(0);
    setPickerMode("list");
    pause();
  }
  const task = tasks[Math.min(active, 2)],
    age = time - (active === 3 ? 30000 : task.start),
    shell = age < 1500;
  const prompt = typed(task.prompt, (age - 2200) / 3000),
    submitted = age >= 5200,
    complete = time >= task.done;
  const shownLines = Math.max(
    0,
    Math.min(task.lines.length, Math.floor((age - 6500) / 1800) + 1),
  );
  const sceneLabel =
    panel === "Governance"
      ? "Review local tool activity"
      : panel === "Account"
        ? "Check cloud usage"
        : active === 3
          ? "All your sessions, one command"
          : shell
            ? "Open a terminal tab"
            : `Work with ${task.name}`;
  return (
    <section
      id="demo"
      ref={root}
      className="workflow-demo"
      aria-label="Fennec and Herdr interactive workflow"
      data-playing={playing && visible && !hidden}
    >
      <div className="workflow-desktop">
        <div className="fennec-window">
          <FennecPanel
            key={replay}
            time={time}
            tab={panel}
            onTab={(value) => {
              pause();
              setPanelOverride(value);
            }}
            onAttach={attach}
            onInteract={pause}
            stopped={stopped}
            onStop={(index) => {
              pause();
              setStopped((value) => [...value, index]);
            }}
          />
        </div>
        <div className="herdr-window">
          <aside className="herdr-sidebar">
            <div className="herdr-brand">
              <div className="mac-dots" aria-hidden="true">
                <i />
                <i />
                <i />
              </div>
              <strong>Herdr</strong>
              <PanelLeft size={15} />
            </div>
            <div className="herdr-sidebar-heading">
              Spaces <Plus size={13} />
            </div>
            <div className="herdr-computer">
              <Monitor size={14} />
              This computer
              <ChevronDown size={12} />
            </div>
            <div className="herdr-project">
              <span>R</span>relay
            </div>
            <div className="herdr-sidebar-heading herdr-agents-heading">
              Agents <span>{sessions.length}</span>
            </div>
            {["Working", "Idle"].map((state) => {
              const group = sessions.filter(
                ({ task }) => sessionState(task, time) === state,
              );
              return (
                group.length > 0 && (
                  <div className="herdr-group" key={state}>
                    <small>
                      {state} <span>{group.length}</span>
                    </small>
                    {group.map(({ task, index }) => (
                      <button
                        key={task.command}
                        className={active === index ? "selected" : ""}
                        onClick={() => attach(index)}
                        title={sessionTitle(task, time)}
                      >
                        <img src={"/icons/" + task.icon} alt="" />
                        <span>{sessionTitle(task, time)}</span>
                        <span
                          className={"herdr-state " + state.toLowerCase()}
                        />
                      </button>
                    ))}
                  </div>
                )
              );
            })}
          </aside>
          <div className="herdr-main">
            <div
              className="herdr-tabs"
              role="tablist"
              aria-label="Herdr terminal tabs"
            >
              {Array.from({ length: current + 1 }, (_, index) => (
                <button
                  role="tab"
                  aria-selected={active === index}
                  aria-controls="demo-terminal"
                  id={"herdr-tab-" + index}
                  key={index}
                  onClick={() => {
                    pause();
                    setTabOverride(index);
                    if (index === 3) setPickerMode("list");
                  }}
                >
                  {index < 3 && time >= tasks[index].start + 1500 ? (
                    <img src={"/icons/" + tasks[index].icon} alt="" />
                  ) : (
                    <Terminal size={14} />
                  )}
                  <span>{index + 1}</span>
                  <X size={12} aria-hidden="true" />
                </button>
              ))}
              <button
                className="herdr-new-tab"
                aria-label="Open next demo terminal tab"
                disabled={current === 3}
                onClick={() => seek((current + 1) * 10000, true)}
              >
                <Plus size={17} />
              </button>
            </div>
            <div
              className="herdr-terminal"
              id="demo-terminal"
              role="tabpanel"
              aria-labelledby={"herdr-tab-" + active}
            >
              <div className="herdr-terminal-heading">
                {shell || active === 3 ? (
                  <Terminal size={15} />
                ) : (
                  <img src={"/icons/" + task.icon} alt="" />
                )}
                <strong>
                  {shell ? "zsh" : active === 3 ? "fen" : task.name}
                </strong>
                <span>~/repos/relay</span>
              </div>
              {(active < 3 && stopped.includes(active)) ||
              (active === 3 && pickerMode === "closed") ? (
                <div className="demo-shell">
                  <p>{active < 3 ? "Session stopped." : ""}</p>
                  <div>
                    ❯ <span className="tui-cursor" />
                  </div>
                  {active === 3 && (
                    <button
                      className="picker-reopen"
                      onClick={() => setPickerMode("list")}
                    >
                      Run fen again
                    </button>
                  )}
                </div>
              ) : shell ? (
                <div className="demo-shell">
                  <p>
                    <span>alex@macbook</span> <b>relay</b> <em>main</em>
                  </p>
                  <div>
                    ❯{" "}
                    {typed(
                      active === 3 ? "fen" : task.command,
                      (age - 400) / 700,
                    )}
                    <span className="tui-cursor" />
                  </div>
                </div>
              ) : active === 3 ? (
                <div
                  className="fen-picker"
                  tabIndex={0}
                  onKeyDown={pickerKey}
                  aria-label="Live session picker. Use arrow keys and Enter to attach."
                >
                  <strong>Attach to session</strong>
                  <p>{sessions.length} live sessions</p>
                  <div className="fen-picker-list">
                    {sessions.map(({ task, index }, row) => (
                      <button
                        key={task.command}
                        className={selected === row ? "selected" : ""}
                        onFocus={() => setSelected(row)}
                        onClick={() => attach(index)}
                      >
                        <span>
                          {selected === row ? "›" : " "} {row + 1}.{" "}
                          {sessionTitle(task, time)}
                        </span>
                        <small>
                          {task.command} · {sessionState(task, time)}
                        </small>
                      </button>
                    ))}
                  </div>
                  <div className="fen-picker-help">
                    {pickerMode === "confirm" ? (
                      <div className="picker-confirm">
                        <p>
                          Stop{" "}
                          {
                            sessions[Math.min(selected, sessions.length - 1)]
                              ?.task.name
                          }
                          ?
                        </p>
                        <button onClick={stopSelected}>Stop session</button>
                        <button onClick={() => setPickerMode("list")}>
                          Cancel
                        </button>
                      </div>
                    ) : (
                      <>
                        ↑/↓ j/k select · enter attach
                        <br />x kill · r refresh · esc quit
                      </>
                    )}
                  </div>
                </div>
              ) : (
                <div className={"agent-tui tui-" + task.command}>
                  <div className="tui-scroll">
                    {active === 0 ? (
                      <div className="codex-welcome">
                        <span>╭──────────────────────────────────────╮</span>
                        <strong>│ &gt;_ OpenAI Codex</strong>
                        <p>│ directory: ~/repos/relay</p>
                        <span>╰──────────────────────────────────────╯</span>
                      </div>
                    ) : active === 1 ? (
                      <div className="claude-welcome">
                        <span>✻</span>
                        <div>
                          <strong>Claude Code</strong>
                          <p>~/repos/relay</p>
                        </div>
                      </div>
                    ) : (
                      <div className="pi-welcome">
                        <strong>pi</strong>
                        <p>~/repos/relay</p>
                        <p>esc to interrupt · ctrl+c twice to exit</p>
                      </div>
                    )}
                    {submitted && (
                      <>
                        <div className="tui-request">
                          <span>{active === 1 ? "❯" : "›"}</span>
                          {task.prompt}
                        </div>
                        <div className="tui-response">{task.response}</div>
                        <div className="tui-output">
                          {task.lines
                            .slice(0, shownLines)
                            .map((line, index) => (
                              <div
                                className={
                                  line.startsWith("ok ") ? "tui-success" : ""
                                }
                                key={line}
                              >
                                <span>{active === 1 ? "⏺" : "•"}</span>
                                <div>
                                  {line.startsWith("go test") ? (
                                    <>
                                      <b>{active === 1 ? "Bash" : "$"}</b>{" "}
                                      {line}
                                    </>
                                  ) : (
                                    line
                                  )}
                                  {index === 0 && <small>~/repos/relay</small>}
                                </div>
                              </div>
                            ))}
                        </div>
                        {complete ? (
                          <p className="tui-result">{task.result}</p>
                        ) : (
                          <p className="tui-working">
                            <span className="tui-spinner" />{" "}
                            {active === 1
                              ? "Thinking…"
                              : active === 2
                                ? "Working…"
                                : "Working"}{" "}
                            <span>esc to interrupt</span>
                          </p>
                        )}
                      </>
                    )}
                  </div>
                  <div className="tui-composer">
                    <span>{active === 1 ? "❯" : "›"}</span>
                    <span>
                      {submitted ? "" : prompt}
                      <span className="tui-cursor" />
                    </span>
                  </div>
                  <div className="tui-footer">
                    <span>
                      {active === 2 ? "relay (main)" : "? for shortcuts"}
                    </span>
                    <span>
                      {active === 0
                        ? "context left · 98%"
                        : active === 1
                          ? "ctrl+t to hide tasks"
                          : "↑↓ history"}
                    </span>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
      <div className="workflow-player">
        <button
          className="workflow-play"
          aria-label={
            playing
              ? "Pause demo"
              : time >= duration
                ? "Replay demo"
                : "Play demo"
          }
          onClick={togglePlay}
        >
          {playing ? <Pause size={15} /> : <Play size={15} />}
        </button>
        <span className="workflow-time">
          {clock(time)} / {clock(duration)}
        </span>
        <input
          aria-label="Demo timeline"
          aria-valuetext={clock(time) + " of " + clock(duration)}
          type="range"
          min="0"
          max={duration}
          step="100"
          value={Math.round(time)}
          onChange={(event) => seek(Number(event.target.value))}
        />
        <span className="workflow-scene" aria-live="polite">
          {sceneLabel}
        </span>
        <button
          className="workflow-replay"
          onClick={restart}
          aria-label="Restart demo"
        >
          <RotateCcw size={15} />
        </button>
      </div>
      <div className="workflow-chapters" aria-label="Demo chapters">
        {chapters.map((chapter, index) => (
          <button
            key={chapter.label}
            aria-current={
              time >= chapter.time &&
              time < (chapters[index + 1]?.time ?? duration + 1)
                ? "step"
                : undefined
            }
            onClick={() => seek(chapter.time, !reduced)}
          >
            {index < 4 && <span>0{index + 1}</span>}
            {chapter.label}
          </button>
        ))}
      </div>
      <p className="workflow-caption">
        Interactive demo · sample sessions and usage.{" "}
        {reduced
          ? "Press play or choose a chapter."
          : "Pause to explore the app and terminal tabs."}
      </p>
    </section>
  );
}

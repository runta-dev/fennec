"use client";
// A browser redraw of the SwiftUI app in ios/Fennec (HomeView, ChatView,
// Components, Theme) at its native point sizes, in light or dark like the
// system, or as chosen in the panel's Appearance. It shows the same sessions
// and timeline as the Mac panel and the recorded terminals.
import { Fragment, useEffect, useLayoutEffect, useRef, type ReactNode } from "react";
import { ArrowUp, ChevronLeft, ChevronRight, Ellipsis, Plus, Search } from "lucide-react";
import { tasks, sessionState, sessionTitle, type Appearance, type Task } from "./demo-scenario";
import { clock, conversation, formatElapsed, preview, toolHeadline, updatedAt, type MobileWork } from "./mobile-scenario";
import "./mobile.css";

type Props = {
  appearance: Appearance; time: number; stopped: number[];
  /** The open conversation, or null for the session list. */
  chat: number | null; tapping: number | null;
  onOpen: (index: number) => void; onBack: () => void;
};

const icons: Record<Task["command"], string> = { codex: "codex", claude: "claudecode", pi: "pi" };

export default function MobileApp({ appearance, time, stopped, chat, tapping, onOpen, onBack }: Props) {
  const device = useRef<HTMLDivElement>(null);
  // Keep the last conversation drawn while it slides back out.
  const shown = useRef(chat ?? 0);
  if (chat !== null) shown.current = chat;
  useEffect(() => {
    const element = device.current!, frame = element.parentElement!;
    const resize = new ResizeObserver(() => {
      const scale = frame.clientWidth / element.offsetWidth;
      element.style.transform = `scale(${scale})`;
      frame.style.height = element.offsetHeight * scale + "px";
    });
    resize.observe(frame);
    return () => resize.disconnect();
  }, []);
  const sessions = tasks.map((task, index) => ({ task, index })).filter(({ task, index }) => time >= task.start + 1500 && !stopped.includes(index));
  return <div className="iphone-frame" data-theme={appearance}>
    <div className="iphone" ref={device}>
      <div className="iphone-screen">
        <StatusBar time={time}/>
        <div className="ios-layer ios-home" data-covered={chat !== null} aria-hidden={chat !== null} inert={chat !== null}>
          <Home time={time} sessions={sessions} tapping={tapping} onOpen={onOpen}/>
        </div>
        <div className="ios-layer ios-chat" data-open={chat !== null} aria-hidden={chat === null} inert={chat === null}>
          <Chat task={tasks[shown.current]} time={time} onBack={onBack}/>
        </div>
        <div className="ios-home-indicator" aria-hidden="true"/>
      </div>
      <div className="iphone-island" aria-hidden="true"/>
    </div>
  </div>;
}

function StatusBar({ time }: { time: number }) {
  return <div className="ios-status" aria-hidden="true">
    <span>{clock(time, false)}</span>
    <span className="ios-status-icons">
      <svg width="18" height="12" viewBox="0 0 18 12"><rect x="0" y="8" width="3" height="4" rx="1"/><rect x="5" y="5.5" width="3" height="6.5" rx="1"/><rect x="10" y="3" width="3" height="9" rx="1"/><rect x="15" y="0" width="3" height="12" rx="1"/></svg>
      <svg width="17" height="12" viewBox="0 0 17 12"><path d="M8.5 2.3c2.3 0 4.4.9 6 2.4l1.2-1.2A10.2 10.2 0 0 0 8.5.6C5.7.6 3.2 1.7 1.3 3.5l1.2 1.2a8.6 8.6 0 0 1 6-2.4Zm0 3.4c1.4 0 2.6.5 3.6 1.4l1.2-1.2a6.9 6.9 0 0 0-9.6 0l1.2 1.2c1-.9 2.2-1.4 3.6-1.4Zm0 3.4c.5 0 1 .2 1.3.5L8.5 11 7.2 9.6c.3-.3.8-.5 1.3-.5Z"/></svg>
      <svg width="27" height="13" viewBox="0 0 27 13"><rect x=".5" y=".5" width="23" height="12" rx="3.8" fill="none" stroke="currentColor" opacity=".4"/><rect x="2" y="2" width="17" height="9" rx="2.5"/><path d="M25 4.5v4c.8-.3 1.3-1.1 1.3-2s-.5-1.7-1.3-2Z" opacity=".45"/></svg>
    </span>
  </div>;
}

function AgentIcon({ task, size, state }: { task: Task; size: number; state?: string }) {
  const name = icons[task.command];
  return <span className={"ios-agent-icon ios-agent-" + name} style={{ width: size, height: size, ["--icon" as string]: `url(/mobile/agent-${name}.png)` }} role="img" aria-label={task.name}>
    {state && <i className={"ios-status-dot " + state.toLowerCase()}/>}
  </span>;
}

function Home({ time, sessions, tapping, onOpen }: { time: number; sessions: { task: Task; index: number }[]; tapping: number | null; onOpen: (index: number) => void }) {
  // SessionStore.groups: Needs input, Working, Idle; newest first.
  const groups = ["Working", "Idle"].map(state => [state, sessions.filter(({ task }) => sessionState(task, time) === state).sort((a, b) => updatedAt(b.task, time) - updatedAt(a.task, time))] as const).filter(([, group]) => group.length);
  return <>
    <div className="ios-scroll ios-list">
      {groups.map(([state, group]) => <Fragment key={state}>
        <div className="ios-group-header">{state}<span>{group.length}</span></div>
        {group.map(({ task, index }) => <button key={task.session} className="ios-session-row" data-pressed={tapping === index} onClick={() => onOpen(index)}>
          <AgentIcon task={task} size={30} state={sessionState(task, time)}/>
          <span className="ios-row-text">
            <span className="ios-row-title"><strong>{sessionTitle(task, time)}</strong><time>{clock(updatedAt(task, time))}</time></span>
            <span className="ios-row-preview">{preview(task, time)}</span>
          </span>
        </button>)}
      </Fragment>)}
    </div>
    <div className="ios-nav">
      <span className="ios-glass ios-circle ios-initials" aria-label="Account">A</span>
      <span className="ios-glass ios-circle" aria-label="New session"><Plus size={19} strokeWidth={2.2}/></span>
    </div>
    <div className="ios-bottom"><span className="ios-glass ios-search"><Search size={18} strokeWidth={2.2}/>Search</span></div>
  </>;
}

function Chat({ task, time, onBack }: { task: Task; time: number; onBack: () => void }) {
  const { prompt, work, finished, activity, elapsed } = conversation(task, time);
  const state = sessionState(task, time);
  const viewport = useRef<HTMLDivElement>(null), content = useRef<HTMLDivElement>(null);
  // defaultScrollAnchor(.bottom): follow the newest message as it grows.
  useLayoutEffect(() => {
    const follow = () => {
      if (!viewport.current || !content.current) return;
      content.current.style.transform = `translateY(${Math.min(0, viewport.current.clientHeight - content.current.offsetHeight)}px)`;
    };
    follow();
    const observer = new ResizeObserver(follow);
    observer.observe(content.current!); observer.observe(viewport.current!);
    return () => observer.disconnect();
  }, [task]);
  // TurnView: while working, the latest three steps; once finished, the reply.
  const visible = finished ? [] : work.slice(-3), summary = finished ? work[work.length - 1] : undefined;
  const latestNote = [...visible].reverse().find(item => item.kind === "agent")?.id;
  const foldable = finished ? work.length > 0 : work.length > 3;
  return <>
    <div className="ios-scroll" ref={viewport}>
      <div className="ios-messages" ref={content}>
        {prompt && <div className="ios-user"><p>{prompt}</p></div>}
        {prompt && <div className="ios-turn-header">
          <span className={finished ? "" : "ios-text-shimmer"}>{finished ? `Worked for ${formatElapsed(elapsed)}` : elapsed < 2 ? "Working" : `Working for ${formatElapsed(elapsed)}`}</span>
          {foldable && <ChevronRight size={13} strokeWidth={2.6}/>}
        </div>}
        {visible.map(item => <Work key={item.id} item={item} clamp={item.id !== latestNote}/>)}
        {activity && <p className="ios-activity">{activity}</p>}
        {summary && <Work key={summary.id} item={summary} clamp={false}/>}
      </div>
    </div>
    <div className="ios-nav">
      <button className="ios-glass ios-circle" aria-label="Back to sessions" onClick={onBack}><ChevronLeft size={22} strokeWidth={2.2}/></button>
      <span className="ios-glass ios-title-pill">
        <AgentIcon task={task} size={24}/>
        <span><strong>{sessionTitle(task, time)}</strong><small><i className={"ios-pill-dot " + state.toLowerCase()}/>{state}</small></span>
      </span>
      <span className="ios-glass ios-circle" aria-label="Session menu"><Ellipsis size={20} strokeWidth={2.2}/></span>
    </div>
    <div className="ios-bottom">
      <div className="ios-glass ios-composer">
        <Plus size={19} strokeWidth={2.2}/>
        <span>Message {task.name}</span>
        {state === "Working"
          ? <i className="ios-send"><svg width="11" height="11" viewBox="0 0 11 11"><rect width="11" height="11" rx="2.2"/></svg></i>
          : <i className="ios-send" data-disabled="true"><ArrowUp size={17} strokeWidth={2.8}/></i>}
      </div>
    </div>
  </>;
}

function Work({ item, clamp }: { item: MobileWork; clamp: boolean }) {
  if (item.kind === "tools") return <p className="ios-step"><span>{middle(toolHeadline(item.commands), 46)}</span><ChevronRight size={11} strokeWidth={2.8}/></p>;
  return <div className={"ios-agent" + (clamp ? " clamped" : "")}>{markdown(item.text)}</div>;
}

/** .truncationMode(.middle) for a single line at the phone's fixed width. */
function middle(text: string, limit: number) {
  return text.length <= limit ? text : text.slice(0, limit - 16) + "…" + text.slice(-15);
}

/** The subset of MarkdownText these replies use: paragraphs, bullet lists,
 *  **bold** and `code`. */
function markdown(text: string) {
  const inline = (line: string) => line.split(/(\*\*[^*]+\*\*|`[^`]+`)/).map((part, i): ReactNode =>
    part.startsWith("**") ? <strong key={i}>{part.slice(2, -2)}</strong> : part.startsWith("`") ? <code key={i}>{part.slice(1, -1)}</code> : part);
  return text.split("\n\n").map((block, i) => {
    const lines = block.split("\n");
    if (lines.every(line => line.startsWith("- "))) return <ul key={i}>{lines.map((line, j) => <li key={j}>{inline(line.slice(2))}</li>)}</ul>;
    return <p key={i}>{inline(block)}</p>;
  });
}

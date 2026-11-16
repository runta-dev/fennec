"use client";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  Activity,
  CheckCircle2,
  ChevronRight,
  Cloud,
  Copy,
  Cpu,
  ExternalLink,
  Globe,
  HardDrive,
  MemoryStick,
  MoreHorizontal,
  Settings,
  ShieldCheck,
  User,
  Wallet,
  CalendarDays,
} from "lucide-react";
import nativeCSS from "../../native/macos/UI/panel.css?raw";
import { agents } from "./agents";
import {
  tasks,
  sessionState,
  sessionTitle,
  type PanelTab,
} from "./demo-scenario";

// Reuse the shipping WebView stylesheet without leaking its global selectors into
// the landing page. This frame has no native bridge and renders sample data only.
const panelAgents = [
  { name: "ChatGPT", cmd: "chatgpt", icon: "chatgpt.svg", cloud: true },
  ...["codex", "claude", "pi", "kimi", "opencode", "grok", "cursor-agent"].map(
    (cmd) => ({ ...agents.find((agent) => agent.cmd === cmd)!, cloud: true }),
  ),
  {
    name: "Claude Desktop",
    cmd: "claude-desktop",
    icon: "claude-color.svg",
    cloud: false,
  },
  {
    name: "Gemini CLI",
    cmd: "gemini",
    icon: "geminicli-color.svg",
    cloud: false,
  },
];
const panelDocument = `<!doctype html><html lang="en" data-theme="light"><head><meta charset="utf-8"><style>${nativeCSS}</style><style>
html,body{height:100%;width:100%}body{border-radius:0}header{height:56px;min-height:56px}.brand img{width:21px;height:21px}.brand{font-size:15px}main{padding-bottom:14px}nav button{padding:3px 6px}.agent-head{height:43px}.billing-field-label{min-width:0}.demo-select{max-width:165px;font:12px -apple-system,BlinkMacSystemFont,sans-serif;color:var(--text);border:1px solid var(--line);border-radius:7px;background:var(--card);padding:6px}.demo-graph{width:100%;height:32px;display:block;margin-top:8px}.demo-stat{min-width:0;padding:0 9px;border-right:1px solid var(--line);font-size:11px;color:var(--muted)}.demo-stat:first-child{padding-left:0}.demo-stat:last-child{border:0}.demo-stat b{display:block;color:var(--text);font-size:13px;font-weight:400;margin-top:8px}.demo-disk{margin-top:14px;font-size:12px;display:flex;justify-content:space-between;color:var(--muted)}.demo-disk span:last-child{color:var(--text)}.demo-meter{height:2px;background:#fff0e0;margin:8px 0 2px}.demo-meter span{display:block;background:var(--orange);height:100%;width:41%}.demo-menu{margin:0 12px 8px;text-align:right}.demo-menu button{font-size:11px}.runtime-row{min-height:49px}.session-actions{grid-template-columns:25px 25px 25px;gap:1px}.session-actions button{width:25px;height:25px}.session-actions .session-copy{width:25px}.session-title{text-align:left}.demo-empty{padding:22px 13px;color:var(--muted);font-size:12px}.demo-notice{height:20px;font-size:10px;color:var(--muted);padding:5px 2px}select:focus-visible{outline:2px solid var(--orange)}
@media(max-width:350px){header{gap:4px}.header-login{max-width:110px}nav{gap:1px}nav button{font-size:11px;padding:3px 4px}.billing-plan-badge{font-size:9px}.demo-select{max-width:140px}.session-actions{grid-template-columns:22px 22px 22px}}
</style></head><body></body></html>`;

export default function FennecPanel({
  time,
  tab,
  onTab,
  onAttach,
  onInteract,
  stopped,
  onStop,
}: {
  time: number;
  tab: PanelTab;
  onTab: (tab: PanelTab) => void;
  onAttach: (index: number) => void;
  onInteract: () => void;
  stopped: number[];
  onStop: (index: number) => void;
}) {
  const [body, setBody] = useState<HTMLElement | null>(null);
  const frame = useRef<HTMLIFrameElement>(null);
  useEffect(() => {
    const element = frame.current;
    const mount = () => setBody(element?.contentDocument?.body ?? null);
    element?.addEventListener("load", mount);
    if (element?.contentDocument?.readyState === "complete") mount();
    return () => element?.removeEventListener("load", mount);
  }, []);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [permission, setPermission] = useState("Approve for me");
  const [filter, setFilter] = useState("All");
  const [menu, setMenu] = useState<number | null>(null);
  const [notice, setNotice] = useState("");
  const sessions = tasks
    .map((task, index) => ({ task, index }))
    .filter(
      ({ task, index }) =>
        time >= task.start + 1500 && !stopped.includes(index),
    );
  useEffect(() => {
    if (!body) return;
    const navigation = body.querySelector("nav")!;
    const update = () => {
      const selected = navigation.querySelector<HTMLElement>(".selected");
      const indicator = navigation.querySelector<HTMLElement>(".tab-indicator");
      if (!selected || !indicator) return;
      indicator.style.width = selected.offsetWidth + "px";
      indicator.style.height = selected.offsetHeight + "px";
      indicator.style.transform =
        "translate(" +
        selected.offsetLeft +
        "px, " +
        selected.offsetTop +
        "px)";
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(navigation);
    return () => observer.disconnect();
  }, [body, tab, sessions.length]);
  async function copy(index: number) {
    onInteract();
    try {
      await navigator.clipboard.writeText("fen attach " + tasks[index].session);
      setNotice("Attach command copied.");
    } catch {
      setNotice("fen attach " + tasks[index].session);
    }
  }
  const audit = [
    {
      at: 7200,
      command: "git status --short",
      directory: "~/repos/relay",
      label: "Allowed",
    },
    {
      at: 12500,
      command: "go test ./internal/webhooks/...",
      directory: "~/repos/relay",
      label: "Allowed",
    },
    {
      at: 17500,
      command: 'rg "workspace_id" internal/auth',
      directory: "~/repos/relay",
      label: "Allowed",
    },
    {
      at: 26500,
      command: "go test ./internal/queue -run TestRetry -count=100",
      directory: "~/repos/relay",
      label: "Allowed",
    },
  ]
    .filter((row) => row.at <= time && filter !== "Blocked")
    .reverse();
  return (
    <iframe
      ref={frame}
      className="native-panel"
      title="Fennec app preview"
      srcDoc={panelDocument}
    >
      {body &&
        createPortal(
          <>
            <header>
              <div className="brand">
                <img src="/fennec.svg" alt="" />
                <strong>Fennec</strong>
              </div>
              <span className="spacer" />
              <button
                className="header-login account-link"
                onClick={() => onTab("Account")}
              >
                alex@relay.dev
              </button>
              <button
                className="icon"
                aria-label="Preview permission settings"
                onClick={() => onTab("Governance")}
              >
                <Settings />
              </button>
            </header>
            <nav aria-label="Fennec panels">
              <span className="tab-indicator" aria-hidden="true" />
              {(["Agents", "Sessions", "Governance", "Account"] as const).map(
                (name) => (
                  <button
                    key={name}
                    className={tab === name ? "selected" : ""}
                    aria-pressed={tab === name}
                    onClick={() => onTab(name)}
                  >
                    {name}
                    {name === "Agents" && <span> {panelAgents.length}</span>}
                    {name === "Sessions" && <span> {sessions.length}</span>}
                  </button>
                ),
              )}
            </nav>
            <main>
              {tab === "Agents" && (
                <section aria-label="Agents">
                  <div className="card">
                    {panelAgents.map((agent) => (
                      <div className="agent" key={agent.cmd}>
                        <button
                          className={
                            "agent-head" +
                            (!agent.cloud ? " agent-coming-soon" : "")
                          }
                          disabled={!agent.cloud}
                          aria-expanded={expanded === agent.cmd}
                          onClick={() => {
                            onInteract();
                            setExpanded(
                              expanded === agent.cmd ? null : agent.cmd,
                            );
                          }}
                        >
                          <img
                            className="agent-icon"
                            src={"/icons/" + agent.icon}
                            alt=""
                          />
                          <span className="agent-name">{agent.name}</span>
                          <span className="detected">
                            {agent.cloud ? "Cloud computer" : "Coming soon"}
                          </span>
                          <span className="chevron">
                            <ChevronRight />
                          </span>
                        </button>
                        {expanded === agent.cmd && (
                          <div className="detail">
                            <div className="takeover">
                              <span className="takeover-name">
                                Run location
                              </span>
                              <span className="runtime-status">
                                Cloud computer
                              </span>
                            </div>
                            {agent.cmd === "chatgpt" ? (
                              <p>Continue your work in the ChatGPT app.</p>
                            ) : (
                              <p>
                                Launch with <code>{agent.cmd}</code> in your
                                terminal.
                              </p>
                            )}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </section>
              )}
              {tab === "Sessions" && (
                <section aria-label="Sessions">
                  <div className="card">
                    {sessions.length === 0 && (
                      <div className="demo-empty">
                        No live sessions. Start an agent in your terminal.
                      </div>
                    )}
                    {["Working", "Idle"].map((state) => {
                      const group = sessions.filter(
                        ({ task }) => sessionState(task, time) === state,
                      );
                      return (
                        group.length > 0 && (
                          <div className="session-group" key={state}>
                            <div className="session-group-heading">
                              {state}
                              <span className="session-group-count">
                                {group.length}
                              </span>
                            </div>
                            {group.map(({ task, index }) => (
                              <div key={task.command}>
                                <div className="runtime-row">
                                  <span className="session-agent-identity">
                                    <img
                                      className="session-agent-icon"
                                      src={"/icons/" + task.icon}
                                      alt={task.name}
                                    />
                                    <span
                                      className="session-state-dot"
                                      data-state={state.toLowerCase()}
                                    />
                                  </span>
                                  <div className="runtime-info">
                                    <button
                                      className="session-title"
                                      style={{
                                        border: 0,
                                        background: "none",
                                        padding: 0,
                                        width: "100%",
                                      }}
                                      onClick={() => onAttach(index)}
                                      title={sessionTitle(task, time)}
                                    >
                                      {sessionTitle(task, time)}
                                    </button>
                                  </div>
                                  <div className="session-actions">
                                    <button
                                      className="session-copy"
                                      aria-label={
                                        "Copy " + task.name + " attach command"
                                      }
                                      onClick={() => copy(index)}
                                    >
                                      <Copy />
                                    </button>
                                    <button
                                      aria-label={"Attach to " + task.name}
                                      onClick={() => onAttach(index)}
                                    >
                                      <ExternalLink />
                                    </button>
                                    <button
                                      aria-label={
                                        task.name + " session actions"
                                      }
                                      aria-expanded={menu === index}
                                      onClick={() => {
                                        onInteract();
                                        setMenu(menu === index ? null : index);
                                      }}
                                    >
                                      <MoreHorizontal />
                                    </button>
                                  </div>
                                </div>
                                {menu === index && (
                                  <div className="demo-menu">
                                    <button
                                      onClick={() => {
                                        onStop(index);
                                        setMenu(null);
                                      }}
                                    >
                                      Stop sample session
                                    </button>
                                  </div>
                                )}
                              </div>
                            ))}
                          </div>
                        )
                      );
                    })}
                  </div>
                  <div className="demo-notice" role="status">
                    {notice}
                  </div>
                </section>
              )}
              {tab === "Governance" && (
                <section aria-label="Governance">
                  <div className="card billing-card permission-card">
                    <div className="billing-row permission-row">
                      <span className="billing-field-label">
                        <span className="billing-field-icon">
                          <ShieldCheck />
                        </span>
                        Permission mode
                      </span>
                      <select
                        className="demo-select"
                        aria-label="Permission mode"
                        value={permission}
                        onChange={(e) => {
                          onInteract();
                          setPermission(e.target.value);
                        }}
                      >
                        {[
                          "Ask for approval",
                          "Approve for me",
                          "Full access",
                        ].map((mode) => (
                          <option key={mode}>{mode}</option>
                        ))}
                      </select>
                    </div>
                  </div>
                  <div className="card runtime-metrics-card audit-card">
                    <div className="runtime-metrics-head">
                      <span className="runtime-metrics-title-wrap">
                        <span className="billing-field-icon">
                          <Activity />
                        </span>
                        <span className="runtime-metrics-title">Activity</span>
                      </span>
                      <select
                        className="demo-select"
                        aria-label="Filter activity"
                        value={filter}
                        onChange={(e) => {
                          onInteract();
                          setFilter(e.target.value);
                        }}
                      >
                        {["All", "Allowed", "Blocked"].map((value) => (
                          <option key={value}>{value}</option>
                        ))}
                      </select>
                    </div>
                    <div className="audit-list">
                      {audit.map((row) => (
                        <div className="audit-entry" key={row.command}>
                          <div className="audit-entry-head">
                            <span className="audit-status-icon">
                              <CheckCircle2 />
                            </span>
                            <div className="audit-entry-body">
                              <div className="audit-entry-top">
                                <span
                                  className="audit-command"
                                  title={row.command}
                                >
                                  {row.command}
                                </span>
                                <span className="audit-time">
                                  {Math.max(
                                    1,
                                    Math.floor((time - row.at) / 1000),
                                  )}
                                  s
                                </span>
                              </div>
                              <span className="audit-meta-text">
                                {row.label} · {row.directory}
                              </span>
                            </div>
                          </div>
                        </div>
                      ))}
                      {audit.length === 0 && (
                        <div className="audit-empty-row">
                          No {filter === "Blocked" ? "blocked " : ""}activity.
                        </div>
                      )}
                    </div>
                  </div>
                </section>
              )}
              {tab === "Account" && (
                <section aria-label="Account">
                  <div className="card runtime-metrics-card">
                    <div className="runtime-metrics-head">
                      <span className="runtime-metrics-title-wrap">
                        <span className="billing-field-icon">
                          <Cloud />
                        </span>
                        <span className="runtime-metrics-title">
                          Cloud computer
                        </span>
                      </span>
                      <span className="runtime-metrics-spec">
                        2 vCPU · 4–16 GiB
                      </span>
                    </div>
                    <div className="runtime-metrics-grid">
                      {[
                        {
                          title: "CPU",
                          icon: <Cpu size={13} />,
                          value: sessions.some(
                            ({ task }) =>
                              sessionState(task, time) === "Working",
                          )
                            ? "24.8%"
                            : "3.2%",
                          path: "0,26 10,26 17,22 25,25 34,24 43,16 53,18 64,5 74,8 85,24 95,23 105,25 116,16 128,21",
                        },
                        {
                          title: "Memory",
                          icon: <MemoryStick size={13} />,
                          value: sessions.length ? "2.4 GB" : "0.8 GB",
                          path: "0,24 17,24 27,20 44,20 53,16 67,17 79,10 98,10 110,11 128,14",
                        },
                      ].map((metric) => (
                        <div className="demo-stat" key={metric.title}>
                          {metric.icon} {metric.title}
                          <b>{metric.value}</b>
                          <svg
                            className="demo-graph"
                            viewBox="0 0 128 34"
                            preserveAspectRatio="none"
                          >
                            <polyline
                              points={metric.path}
                              fill="none"
                              stroke="#f38d25"
                              strokeWidth="1.6"
                            />
                          </svg>
                        </div>
                      ))}
                      <div className="demo-stat">
                        <Globe size={13} /> Network<b>↑ 1.24 GB</b>
                        <b>↓ 3.86 GB</b>
                      </div>
                    </div>
                    <div className="demo-disk">
                      <span>
                        <HardDrive size={13} /> Disk
                      </span>
                      <span>12.8 GB / 31.3 GB</span>
                    </div>
                    <div className="demo-meter">
                      <span />
                    </div>
                  </div>
                  <div className="card billing-card">
                    <div className="billing-row">
                      <span className="billing-field-label">
                        <span className="billing-field-icon">
                          <User />
                        </span>
                        alex@relay.dev
                      </span>
                      <span className="billing-plan-badge">Pay-as-you-go</span>
                    </div>
                    <div className="billing-row">
                      <span className="billing-field-label">
                        <span className="billing-field-icon">
                          <Wallet />
                        </span>
                        Available balance
                      </span>
                      <span>$24.60</span>
                    </div>
                    <div className="billing-row">
                      <span className="billing-field-label">
                        <span className="billing-field-icon">
                          <CalendarDays />
                        </span>
                        Last 30 days spend
                      </span>
                      <span>$12.40</span>
                    </div>
                    <div className="billing-resources">
                      {[
                        ["CPU", "82.6 vCPU-h"],
                        ["Memory", "164.2 GiB-h"],
                        ["Disk", "896.4 GiB-h"],
                      ].map(([label, value]) => (
                        <div className="billing-row" key={label}>
                          <span className="billing-field-label">{label}</span>
                          <span className="billing-value">{value}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                </section>
              )}
            </main>
          </>,
          body,
        )}
    </iframe>
  );
}

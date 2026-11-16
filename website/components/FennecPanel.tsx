"use client";
import { useEffect, useRef, useState } from "react";
import { tasks, sessionState, sessionTitle, type PanelTab } from "./demo-scenario";

type Props = {
  time: number; tab: PanelTab; onTab: (tab: PanelTab) => void;
  onAttach: (index: number) => void; onInteract: () => void;
  stopped: number[]; onStop: (index: number) => void;
};

export default function FennecPanel(props: Props) {
  const frame = useRef<HTMLIFrameElement>(null);
  const latest = useRef(props);
  latest.current = props;
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || event.origin !== location.origin || event.data?.source !== "fennec-demo") return;
      const { action, value } = event.data, p = latest.current;
      const index = tasks.findIndex(t => t.session === value);
      if (action === "ready") setReady(true);
      if (action === "interact") p.onInteract();
      if (action === "tab" && ["agents", "sessions", "governance", "account"].includes(value)) p.onTab((value[0].toUpperCase() + value.slice(1)) as PanelTab);
      if (action === "attach" && index >= 0) p.onAttach(index);
      if (action === "stop" && index >= 0) p.onStop(index);
      if (action === "copy" && index >= 0) navigator.clipboard?.writeText("fen attach " + value).then(() => frame.current?.contentWindow?.postMessage({ source: "fennec-demo-host", copied: value }, location.origin)).catch(() => {});
    };
    window.addEventListener("message", receive);
    const element = frame.current!;
    // A fast cached frame can finish loading before React hydrates.
    const loaded = () => { if (element.contentDocument?.readyState === "complete" && element.contentDocument.querySelector("#agent-list .agent")) setReady(true); };
    element.addEventListener("load", loaded);
    loaded();
    const resize = new ResizeObserver(() => {
      const width = element.parentElement!.clientWidth, scale = width / 440;
      element.style.width = "440px";
      element.style.height = element.parentElement!.clientHeight / scale + "px";
      element.style.transform = `scale(${scale})`;
    });
    resize.observe(element.parentElement!);
    return () => { window.removeEventListener("message", receive); element.removeEventListener("load", loaded); resize.disconnect(); };
  }, []);
  const { time, tab, stopped } = props;
  const stateKey = tasks.map((t, i) => `${time >= t.start + 1500 && !stopped.includes(i)}:${sessionState(t, time)}:${sessionTitle(t, time)}`).join("|");
  useEffect(() => {
    if (!ready) return;
    const p = latest.current;
    const sessions = tasks.filter((t, i) => p.time >= t.start + 1500 && !p.stopped.includes(i)).map(t => ({
      address: t.session, state: "live", agent: t.command, title: sessionTitle(t, p.time),
      status: { state: sessionState(t, p.time).toLowerCase() },
      labels: { "fennec.session": t.session, "fennec.agent": t.command },
    }));
    const activity = ["uname -s && /opt/homebrew/bin/go test -race ./...", "cat go.mod relay.go relay_test.go AGENTS.md", "/opt/homebrew/bin/go test ./... -run TestRetryDelayEdgeCases -count=1", "git diff --stat"].map((command, i) => ({
      id: "demo-audit-" + i, kind: "exec", command, workdir: "/tmp/relay-mac-demo", decision: "auto_sandbox", blocked: false, timestamp_ms: Date.now() - (i + 1) * 8000,
    }));
    frame.current?.contentWindow?.postMessage({ source: "fennec-demo-host", panel: p.tab.toLowerCase(), sessions, activity }, location.origin);
  }, [ready, tab, stateKey]);
  return <iframe ref={frame} className="native-panel" title="Fennec" src="/demo-native/index.html" sandbox="allow-scripts allow-same-origin" style={{ transformOrigin: "top left" }} />;
}

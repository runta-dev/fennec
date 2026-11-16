// Timing and native-panel fixtures accompanying real PTY excerpts.
export const duration = 44000;
export const tasks = [
  { name: "Codex", command: "codex", icon: "codex-color.svg", session: "maple-river", start: 0, done: 12761, title: "Make webhook delivery idempotent" },
  { name: "Claude Code", command: "claude", icon: "claudecode-color.svg", session: "cedar-moon", start: 10000, done: 28000, title: "Review API key authentication" },
  { name: "Pi", command: "pi", icon: "pi.svg", session: "birch-lake", start: 20000, done: 33000, title: "Add retry backoff tests" },
] as const;
export type Task = (typeof tasks)[number];
export type PanelTab = "Agents" | "Sessions" | "Governance" | "Account";
export function sessionState(task: Task, time: number) { return time >= task.done ? "Idle" : time >= task.start + 2500 ? "Working" : "Idle"; }
export function sessionTitle(task: Task, time: number) { return time >= task.start + 2500 ? task.title : "Untitled session"; }

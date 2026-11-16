// Timing and native-panel fixtures accompanying real PTY excerpts.
export const pickerStart = 52000;
export const pickerDuration = 5000;
export const duration = 66000;
export const tasks = [
  { name: "Codex", command: "codex", icon: "codex-color.svg", session: "maple-river", start: 0, done: 18000, title: "Fix webhook deliveries on my Mac" },
  { name: "Claude Code", command: "claude", icon: "claudecode-color.svg", session: "cedar-moon", start: 18000, done: 36000, title: "Review authorization on my Mac" },
  { name: "Pi", command: "pi", icon: "pi.svg", session: "birch-lake", start: 36000, done: 52000, title: "Run retry tests on my Mac" },
] as const;
export type Task = (typeof tasks)[number];
export type PanelTab = "Agents" | "Sessions" | "Governance" | "Account";
export function sessionState(task: Task, time: number) { return time >= task.done ? "Idle" : time >= task.start + 2500 ? "Working" : "Idle"; }
export function sessionTitle(task: Task, time: number) { return time >= task.start + 2500 ? task.title : "Untitled session"; }

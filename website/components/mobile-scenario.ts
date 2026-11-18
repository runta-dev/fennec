// What the iPhone app shows for each recorded session. Every line is taken
// from the PTY recordings in public/demo-recordings (prompts.json for the
// prompts), and each `at` is the recording time in ms at which it appears, so
// the phone stays in step with the terminal. Tool runs are the agent's
// commands; consecutive runs fold into one step, as in the app.
import { tasks, type Task } from "./demo-scenario";

export type MobileItem = { at: number; kind: "agent" | "tool"; text: string };
export type MobileScript = {
  prompt: string; promptAt: number;
  /** Reported turn length in seconds ("Worked for …"). */
  worked: number;
  /** Herdr's line about what the agent is doing, shown while it works. */
  activity: [number, string][];
  items: MobileItem[];
};

const mac = "ssh Alexs-Mac.local 'cd /tmp/relay-mac-demo && ";

export const scripts: Record<Task["command"], MobileScript> = {
  codex: {
    prompt: "Fix duplicate webhook deliveries in my Mac project, then run its tests on my Mac.", promptAt: 1200, worked: 35,
    activity: [[9500, "Adding map guard tests"]],
    items: [
      { at: 3000, kind: "agent", text: "I’ll read the webhook code on your Mac, fix the duplicate deliveries, and run the tests there." },
      { at: 4500, kind: "tool", text: mac + "for f in AGENTS.md go.mod *.go; do …'" },
      { at: 8600, kind: "agent", text: "The delivery handler increments the count every time it receives an event, even when that event ID was already processed. I’ll add a duplicate check under the existing mutex and test both repeated and concurrent deliveries." },
      { at: 9000, kind: "tool", text: mac + "/usr/bin/perl -0pi -e …'" },
      { at: 13500, kind: "tool", text: "ssh Alexs-Mac.local 'uname -s && cd /tmp/relay-mac-demo && /opt/homebrew/bin/go test -race ./...'" },
      { at: 14700, kind: "agent", text: "Fixed duplicate webhook deliveries in `/tmp/relay-mac-demo` on your Mac. Repeated event IDs are now ignored under the existing mutex. Added tests for repeated and concurrent deliveries.\n\nAll tests passed with `go test -race ./...`; `uname -s` confirmed `Darwin`." },
    ],
  },
  claude: {
    prompt: "Review authorization in my Mac project. Fix any cross-workspace access bug and verify it with tests on my Mac.", promptAt: 1200, worked: 45,
    activity: [[7400, "Fixing Authorize and adding tests"]],
    items: [
      { at: 3500, kind: "agent", text: "I'll start by listing the fixture's files in `/tmp/relay-mac-demo` on your Mac over SSH." },
      { at: 3600, kind: "tool", text: mac + "find . -type f -not -path \"./.git/*\" | head -50'" },
      { at: 4966, kind: "tool", text: mac + "cat go.mod relay.go relay_test.go AGENTS.md'" },
      { at: 6500, kind: "agent", text: "`Authorize` in `relay.go` only checks revocation and never compares workspaces, so a key from workspace A passes in workspace B. I'm fixing that and adding tests for the cross-workspace and revoked cases." },
      { at: 7500, kind: "tool", text: mac + "/usr/bin/perl -0pi -e \"s/func Authorize…\" relay.go'" },
      { at: 10400, kind: "agent", text: "The edit is in and gofmt reports no formatting problems. Now I'll run the tests on the Mac, with the race detector on:" },
      { at: 11000, kind: "tool", text: mac + "uname -s && /opt/homebrew/bin/go test -race -v ./... 2>&1 | tail -20'" },
      { at: 14500, kind: "agent", text: "I fixed a cross-workspace access bug in your Mac project at `/tmp/relay-mac-demo`. All tests pass, and `uname -s` printed `Darwin`.\n\n**The bug:** `Authorize(key, workspace)` in `relay.go` only checked that the key wasn't revoked, so a valid key from `acme` was accepted in `globex`.\n\n**The fix:** a key must not be revoked and must belong to the workspace being requested.\n\nNothing was committed." },
    ],
  },
  pi: {
    prompt: "Add retry edge-case tests to my Mac project. Fix any failures and run the tests on my Mac.", promptAt: 645, worked: 62,
    activity: [[3000, "Retrying the SSH connection"], [7900, "Implementing append-table extremes"]],
    items: [
      { at: 1950, kind: "agent", text: "I’ll inspect the retry code and existing tests on your Mac, add coverage for edge cases, and fix any failures they reveal." },
      { at: 2184, kind: "tool", text: mac + "pwd && rg --files'" },
      { at: 3030, kind: "tool", text: "ssh -o ConnectTimeout=10 -o BatchMode=yes Alexs-Mac.local 'cd /tmp/relay-mac-demo && /usr/bin/find . -type f -maxdepth 3 -print'" },
      { at: 3801, kind: "agent", text: "The SSH connection to your Mac has timed out twice. I’ll try once more; I haven’t been able to read or change any files yet." },
      { at: 4000, kind: "tool", text: "ssh -o ConnectTimeout=10 -o ConnectionAttempts=1 -o BatchMode=yes Alexs-Mac.local 'cd /tmp/relay-mac-demo && /usr/bin/sed -n \"1,160p\" go.mod'" },
      { at: 4414, kind: "tool", text: mac + "/usr/bin/sed -n \"1,180p\" AGENTS.md relay.go relay_test.go'" },
      { at: 7847, kind: "agent", text: "`RetryDelay` caps large attempts at 32 seconds, but negative attempts cause a shift panic. I’ll add tests for the backoff sequence, cap boundaries, and extreme integers, then make negative attempts use the initial one-second delay." },
      { at: 7900, kind: "tool", text: mac + "/bin/cat >> relay_test.go <<'EOF' …'" },
      { at: 10641, kind: "tool", text: mac + "/opt/homebrew/bin/gofmt -w relay.go && uname -s && /opt/homebrew/bin/go test -race ./... -count=1'" },
      { at: 11886, kind: "agent", text: "Completed directly on your Mac in `/tmp/relay-mac-demo`.\n\n- Added 10 retry test cases covering negative attempts, the exponential sequence, cap boundaries, and minimum/maximum integers.\n- Confirmed negative attempts caused a panic, then fixed `RetryDelay` to return the initial one-second delay for them.\n- Ran the full suite with the race detector.\n\nVerification printed `Darwin`, and all tests passed: `ok relay`. No commit was created." },
    ],
  },
};

/** Work in the conversation so far: notes, and consecutive commands as one step. */
export type MobileWork = { id: string; kind: "agent"; text: string } | { id: string; kind: "tools"; commands: string[] };

export function conversation(task: Task, time: number) {
  const script = scripts[task.command], age = time - task.start, length = task.done - task.start;
  const work: MobileWork[] = [];
  script.items.forEach((item, index) => {
    if (age < item.at) return;
    const last = work[work.length - 1];
    if (item.kind === "tool" && last?.kind === "tools") last.commands.push(item.text);
    else work.push(item.kind === "tool" ? { id: task.session + index, kind: "tools", commands: [item.text] } : { id: task.session + index, kind: "agent", text: item.text });
  });
  const finished = time >= task.done;
  // Herdr's line describes work in progress; it ends with the closing reply.
  const replied = age >= script.items[script.items.length - 1].at;
  const activity = finished || replied ? undefined : script.activity.filter(([at]) => age >= at).pop()?.[1];
  // The demo compresses each turn; scale the clock so it ends on the reported length.
  const elapsed = finished ? script.worked : Math.max(0, Math.round((age - script.promptAt) / (length - script.promptAt) * script.worked));
  return { prompt: age >= script.promptAt ? script.prompt : undefined, work, finished, activity, elapsed };
}

/** Same rule as Session.preview: Herdr's line while working, else the latest reply's first line. */
export function preview(task: Task, time: number) {
  const { prompt, work, activity } = conversation(task, time);
  if (activity) return activity;
  const reply = [...work].reverse().find(item => item.kind === "agent");
  if (reply?.kind === "agent") return plainLine(reply.text);
  return prompt ?? "";
}

export function plainLine(markdown: string) {
  const line = markdown.split("\n").map(value => value.trim()).find(Boolean) ?? "";
  return line.replace(/^(#{1,6}|[-*+]|>)\s+/, "").replace(/\*\*|`/g, "");
}

/** A wall clock for the sample: 3:24 PM when the walkthrough starts. */
export function clock(time: number, meridiem = true) {
  const minutes = 24 + Math.floor(time / 6000);
  return `3:${String(minutes).padStart(2, "0")}${meridiem ? " PM" : ""}`;
}

/** Last time a session changed: now while it works, then when it finished. */
export function updatedAt(task: Task, time: number) { return Math.min(time, task.done); }

/** The conversation the phone has open by default: each task from just after
 *  its title appears until shortly after it finishes. */
export function autoChat(time: number) {
  const index = tasks.findIndex(task => time >= task.start + 2500 && time < task.done + 1200);
  return index < 0 ? null : index;
}

/** Elapsed time like WorkingIndicator.elapsed: 35s, 1m 2s, 1h 3m. */
export function formatElapsed(seconds: number) {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`;
}

/** ToolUse.headline for commands: one is named, several are counted. */
export function toolHeadline(commands: string[]) {
  return commands.length === 1 ? "Ran " + commands[0] : `Ran ${commands.length} commands`;
}

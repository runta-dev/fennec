// Presentation data only: no commands, credentials or real sessions leave the browser.
export const duration = 44000;
export const tasks = [
  {
    name: "Codex",
    command: "codex",
    icon: "codex-color.svg",
    session: "maple-river",
    start: 0,
    done: 26000,
    title: "Make Stripe webhooks idempotent",
    prompt:
      "Make the Stripe webhook handler idempotent. Use event IDs, and cover duplicate deliveries with a test.",
    response:
      "I’ll check the handler and event store, then add a regression test for duplicate deliveries.",
    lines: [
      "Read internal/webhooks/stripe.go",
      "Added a unique event ID check inside the transaction",
      "Added TestDuplicateDelivery",
      "go test ./internal/webhooks/...",
      "ok   relay/internal/webhooks   0.42s",
    ],
    result:
      "Duplicate deliveries now return 200 without processing twice. The regression test passes.",
  },
  {
    name: "Claude Code",
    command: "claude",
    icon: "claudecode-color.svg",
    session: "cedar-moon",
    start: 10000,
    done: 34000,
    title: "Review API key authentication",
    prompt:
      "Review the API key middleware for auth bypasses. Focus on revoked keys and workspace isolation. Don’t edit files.",
    response:
      "I’ll trace the middleware and its tests, then report any concrete security issues.",
    lines: [
      "Read internal/auth/middleware.go",
      "Read internal/auth/keys_test.go",
      "Checked revoked-key handling and workspace scope",
      "Found a missing workspace check on the export route",
    ],
    result:
      "One finding: the export route accepts a key from another workspace. Suggested a scope check and regression test.",
  },
  {
    name: "Pi",
    command: "pi",
    icon: "pi.svg",
    session: "birch-lake",
    start: 20000,
    done: 39000,
    title: "Fix the flaky retry test",
    prompt:
      "The queue retry test flakes in CI. Replace the wall-clock sleeps with a fake clock, then run it 100 times.",
    response:
      "I’ll reproduce the timing issue and make the retry test deterministic.",
    lines: [
      "Read internal/queue/retry_test.go",
      "Replaced time.Sleep with the test clock",
      "go test ./internal/queue -run TestRetry -count=100",
      "ok   relay/internal/queue   1.86s",
    ],
    result: "The retry test now uses a fake clock. All 100 runs pass.",
  },
] as const;
export type Task = (typeof tasks)[number];
export type PanelTab = "Agents" | "Sessions" | "Governance" | "Account";
export const chapters = [
  { label: "codex", time: 0 },
  { label: "claude", time: 10000 },
  { label: "pi", time: 20000 },
  { label: "fen", time: 30000 },
  { label: "Governance", time: 35000 },
  { label: "Account", time: 39000 },
];
export function sessionState(task: Task, time: number) {
  return time >= task.done
    ? "Idle"
    : time >= task.start + 5200
      ? "Working"
      : "Idle";
}
export function sessionTitle(task: Task, time: number) {
  return time >= task.start + 5200 ? task.title : "Untitled session";
}
export function typed(text: string, progress: number) {
  return text.slice(
    0,
    Math.max(0, Math.floor(text.length * Math.min(1, progress))),
  );
}

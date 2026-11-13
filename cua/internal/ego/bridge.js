// fennec-cua's bridge into ego lite. The daemon runs this script with
// `ego-browser nodejs -e`, after a first line that defines SOCK (the
// daemon's unix socket) and TOKEN (proof that this process is the one it
// started). The runtime does not forward stdin, so the script connects back
// to the daemon and serves JSON lines: one request per line, one reply per
// line. It only dispatches to the public SDK (taskSpace, listTaskSpaces,
// takeOverTaskSpace, TaskSpace and Page methods); every decision is the
// daemon's. When the daemon closes the socket the script exits.
const net = await import("node:net");

const spaces = new Map(); // spaceId -> TaskSpace

function plain(value) {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const proto = Object.getPrototypeOf(value);
    if (proto !== Object.prototype && proto !== null) {
      // An SDK handle (a Page from newPage) answers its identity.
      const id = {};
      for (const k of ["label", "targetId", "spaceId", "name", "ownership"]) {
        if (typeof value[k] === "string" || typeof value[k] === "number") {
          id[k] = value[k];
        }
      }
      return id;
    }
  }
  // Nested Page handles and other SDK objects are not data: drop them.
  return JSON.parse(JSON.stringify(value === undefined ? null : value, (key, v) => {
    if (v && typeof v === "object" && !Array.isArray(v)) {
      const proto = Object.getPrototypeOf(v);
      if (proto !== Object.prototype && proto !== null) {
        return undefined;
      }
    }
    return typeof v === "function" ? undefined : v;
  }));
}

function info(task) {
  spaces.set(task.spaceId, task);
  return { spaceId: task.spaceId, name: task.name, ownership: task.ownership };
}

function space(id) {
  const task = spaces.get(id);
  if (!task) {
    throw new Error("space " + id + " is not open in this bridge");
  }
  return task;
}

function method(target, path) {
  const parts = String(path).split(".");
  let owner = target;
  for (let i = 0; i < parts.length - 1; i++) {
    owner = owner[parts[i]];
    if (!owner) {
      throw new Error("no " + path);
    }
  }
  const fn = owner[parts[parts.length - 1]];
  if (typeof fn !== "function") {
    throw new Error("no " + path);
  }
  return fn.bind(owner);
}

async function serve(req) {
  const a = req.args || {};
  switch (req.op) {
    case "space":
      return info(await taskSpace(a.name));
    case "resume":
      return info(await taskSpace(a.spaceId));
    case "takeover":
      return info(await takeOverTaskSpace(a.spaceId));
    case "spaces":
      return plain(await listTaskSpaces());
    case "adopt": {
      const task = space(a.spaceId);
      const tab = (await task.tabs()).find((t) => t.targetId === a.targetId);
      if (!tab) {
        throw new Error("the tab is gone");
      }
      if (tab.label) {
        return { label: tab.label };
      }
      const page = await task.adopt(tab.page);
      return { label: page.label };
    }
    case "call": {
      const task = space(a.spaceId);
      const target = a.page ? task.page(a.page) : task;
      return plain(await method(target, a.method)(...(a.args || [])));
    }
  }
  throw new Error("unknown op " + req.op);
}

const sock = net.createConnection(SOCK);
sock.setEncoding("utf8");
sock.on("connect", () => sock.write(JSON.stringify({ hello: TOKEN }) + "\n"));
sock.on("error", () => process.exit(1));
sock.on("close", () => process.exit(0));
let buf = "";
sock.on("data", (chunk) => {
  buf += chunk;
  let nl;
  while ((nl = buf.indexOf("\n")) >= 0) {
    const line = buf.slice(0, nl);
    buf = buf.slice(nl + 1);
    if (line.trim() === "") {
      continue;
    }
    let req;
    try {
      req = JSON.parse(line);
    } catch (e) {
      continue;
    }
    if (req.op === "bye") {
      sock.destroy();
      process.exit(0);
    }
    serve(req).then(
      (value) => sock.write(JSON.stringify({ id: req.id, ok: true, value: value === undefined ? null : value }) + "\n"),
      (err) => sock.write(JSON.stringify({ id: req.id, ok: false, error: { message: String((err && err.message) || err), code: (err && err.code) || "" } }) + "\n"),
    );
  }
});

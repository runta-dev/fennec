import { ArrowUpRight, Download, ScanEye, Plug, ShieldCheck } from 'lucide-react';
import { SiteHeader, SiteFooter, repo, type Brand } from '../../components/SiteChrome';

export const metadata = {
  alternates: { canonical: '/computer-use/' },
  icons: { icon: '/computer-use-icon.png', apple: '/computer-use-icon.png' },
  title: 'Fennec Computer Use — Let your agent use your Mac.',
  description: 'Fennec Computer Use lets Claude Code, Codex and other MCP agents operate Mac apps and web pages. A notarized Mac app; drag it to Applications.',
  openGraph: { url: 'https://fennec.runta.com/computer-use/', title: 'Fennec Computer Use', description: 'Let your agent use your Mac: Mac apps and web pages for Claude Code, Codex and other MCP agents.', type: 'website', siteName: 'Fennec', images: [{ url: '/computer-use-icon.png', alt: 'Fennec Computer Use' }] },
};

// Fennec Computer Use is its own product: its mark (the fox with a pointer
// badge, the app's icon), its name in the header and footer, and a way back
// to Fennec in the navigation.
const brand: Brand = {
  name: 'Fennec', sub: 'Computer Use', logo: '/computer-use-icon.png', home: '/computer-use/',
  nav: [{ label: 'Fennec', href: '/' }, { label: 'Benchmark', href: '#benchmark' }, { label: 'FAQ', href: '#faq' }],
};

// Downloads come from R2 through the site's worker (deploy/worker.js).
const download = '/download/computer-use/latest';
const results = repo + '/blob/main/cua/bench/RESULTS.md';
const releases = repo + '/releases?q=fennec-cua&expanded=true';

// The held-out set of cua/bench/RESULTS.md (2026-11-16): 60 macOSWorld tasks
// never used for tuning, 3 trials per backend, one golden macOS 26 image.
const heldOut = [
  { name: 'Fennec Computer Use', ours: true, solved: 123, scored: 179, tokens: '277k', turns: '19.2' },
  { name: 'cua-driver', note: 'raw MCP tools', solved: 116, scored: 179, tokens: '862k', turns: '20.9' },
  { name: 'Shuttle', note: '0.1.50', solved: 105, scored: 178, tokens: '202k', turns: '23.3' },
];
const tuned = [
  { name: 'Fennec Computer Use', ours: true, dev: '91.1%', safety: '77.2%', distracted: '0%' },
  { name: 'cua-driver', dev: '81.1%', safety: '71.9%', distracted: '14%' },
  { name: 'Shuttle', dev: '80.0%', safety: '61.4%', distracted: '11%' },
];
const pct = (a: number, b: number) => (100 * a / b).toFixed(1) + '%';

function DownloadButton({ label = 'Download Fennec Computer Use' }: { label?: string }) {
  return <a className="button primary" href={download}><Download size={17}/>{label}</a>;
}

export default function ComputerUse() {
  return <>
    <SiteHeader brand={brand} cta={{ label: 'Download', href: download }}/>
    <main>
      <section className="hero cu-hero">
        <img className="cu-mark" src="/computer-use-icon.png" width="88" height="88" alt=""/>
        <h1>Fennec <span>Computer Use</span></h1>
        <p className="cu-tagline">Let your agent use your Mac.</p>
        <p>Claude Code, Codex or any MCP agent can operate the apps and web pages on your Mac. <br/>It reads each app the way it is built, not by guessing from pixels.</p>
        <div className="hero-actions"><DownloadButton/><a className="button secondary" href="#benchmark">See the benchmark</a></div>
        <span className="platform-note">A separate app from Fennec · works on its own · macOS 13 or later · Apple Silicon</span>
      </section>

      <section className="intro-section cu-intro">
        <h2>One app on your Mac.<br/><span>Any agent can use it.</span></h2>
        <p>Fennec Computer Use runs on its own, without the Fennec app.<br/>Your agent drives it through one JavaScript REPL, compatible with the Codex computer-use API.</p>
        <div className="feature-grid">
          <article><ScanEye size={22} strokeWidth={1.5}/><h3>Reads apps, not pixels.</h3><p>Your agent sees each window's controls through macOS accessibility, acts on them by id, and reads the change back. Screenshots are there when it needs them.</p></article>
          <article><Plug size={22} strokeWidth={1.5}/><h3>Works with your agent.</h3><p>One click connects Claude Code or Codex: the setup window adds the MCP server and a skill that teaches the API. Web pages open in its own Chromium, apart from your browser.</p></article>
          <article><ShieldCheck size={22} strokeWidth={1.5}/><h3>Careful by default.</h3><p>It hands back anything that needs your password or a purchase, asks before it deletes or sends, and closes pop-ups it did not open instead of following them.</p></article>
        </div>
      </section>

      <section className="cu-bench" id="benchmark">
        <div className="eyebrow">BENCHMARK</div>
        <h2>More tasks done.<br/><span>A third of the tokens.</span></h2>
        <p className="cu-bench-lead">macOSWorld tasks on real Mac apps, each run in a fresh macOS 26 virtual machine and graded only by macOSWorld's own checks. Every backend gets the same agent loop, the same model (GLM-5.3 FlashX) and the same 40-turn budget.</p>
        <div className="cu-table-card">
          <div className="cu-table-head"><strong>Held-out tasks</strong><span>60 tasks never used for tuning · 3 trials each</span></div>
          <table className="cu-table">
            <thead><tr><th scope="col">Backend</th><th scope="col">Tasks completed</th><th scope="col" className="num">Tokens per run</th><th scope="col" className="num">Turns per run</th></tr></thead>
            <tbody>{heldOut.map(r => <tr key={r.name} className={r.ours ? 'ours' : undefined}>
              <th scope="row">{r.name}{r.note && <small>{r.note}</small>}</th>
              <td><div className="cu-score"><span className="cu-bar"><i style={{ width: (100 * r.solved / r.scored) + '%' }}/></span><b>{pct(r.solved, r.scored)}</b><small>{r.solved}/{r.scored}</small></div></td>
              <td className="num">{r.tokens}</td><td className="num">{r.turns}</td>
            </tr>)}</tbody>
          </table>
          <p className="cu-table-note">Paired by task: +10.0 points over Shuttle (95% interval +0.7 to +19.2); +4.6 over cua-driver (−2.7 to +12.2, within noise) with about a third of its tokens.</p>
        </div>
        <div className="cu-table-card">
          <div className="cu-table-head"><strong>Development and safety sets</strong><span>used while tuning Fennec Computer Use, so they favour it</span></div>
          <table className="cu-table">
            <thead><tr><th scope="col">Backend</th><th scope="col" className="num">Development (30 tasks)</th><th scope="col" className="num">Safety (19 tasks)</th><th scope="col" className="num">Fell for a pop-up</th></tr></thead>
            <tbody>{tuned.map(r => <tr key={r.name} className={r.ours ? 'ours' : undefined}>
              <th scope="row">{r.name}</th><td className="num">{r.dev}</td><td className="num">{r.safety}</td><td className="num">{r.distracted}</td>
            </tr>)}</tbody>
          </table>
          <p className="cu-table-note">Safety tasks open a misleading dialog at the start; the last column is how often an agent clicked its action instead of dismissing it.</p>
        </div>
        <a className="text-link" href={results}>Method, per-set results and caveats <ArrowUpRight size={15}/></a>
      </section>

      <section className="getting-started" id="install">
        <div><div className="eyebrow">GET STARTED</div><h2>Three steps.<br/>No terminal.</h2><p>Open the disk image and drag the app to Applications. When you open it, its setup window walks you through the two macOS permissions, then connects Claude Code or Codex with one click.</p><span className="preview-note">Prefer a script? The <a className="text-link-inline" href={releases}>releases</a> also carry archives and checksums.</span></div>
        <div className="cli-card cu-steps">
          <ol>
            <li><b>Drag to Applications.</b><small>Open the downloaded .dmg and drag FennecCUA into Applications.</small></li>
            <li><b>Open FennecCUA and allow access.</b><small>Accessibility and Screen Recording, so your agent can read and operate apps.</small></li>
            <li><b>Connect your agent.</b><small>Click Connect next to Claude Code or Codex. It adds the tools and the skill for you.</small></li>
          </ol>
        </div>
      </section>

      <section className="faq-section" id="faq"><h2>A few things to know.</h2><div>{[
        ['How is this different from Fennec?', 'Fennec runs your coding agents in the cloud, connected to your Mac. Fennec Computer Use lets an agent operate the apps on this Mac. They are separate apps: use either one, or both together.'],
        ['Do I need the Fennec app?', 'No. Fennec Computer Use installs on its own as the FennecCUA app. If you also use Fennec, both share the same settings.'],
        ['Which agents work with it?', 'Any agent that speaks MCP. The setup window connects Claude Code and Codex for you; other MCP clients take one config entry, and agents with only a shell can call fennec-cua js.'],
        ['What can my agent see?', 'Only what it asks for: the windows of the apps it works in, as a list of controls or a screenshot,, sent to your agent and the model it uses.'],
        ['Can I stop it?', 'Yes. macOS shows when an app uses the screen, you can revoke its permissions in System Settings at any time, and Stop halts whatever the agent is doing.'],
        ['What does it run on?', 'Apple Silicon Macs with macOS 13 or later. The app is signed and notarized by Apple.'],
      ].map(([q, a]) => <details key={q}><summary>{q}<span className="faq-plus" aria-hidden="true">+</span></summary><p>{a}</p></details>)}</div></section>

      <section className="final-cta"><img src="/computer-use-icon.png" width="72" height="72" alt="Fennec Computer Use"/><h2>Give your agent a hand.</h2><p>Fennec Computer Use: drag it to Applications, click Connect. Your agent does the rest.</p><DownloadButton/><span className="platform-note">A separate app from Fennec · macOS 13 or later · Apple Silicon</span></section>
    </main>
    <SiteFooter brand={brand} tagline="Fennec Computer Use: let your agent use your Mac."/>
  </>;
}

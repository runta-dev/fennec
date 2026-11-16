import { ArrowUpRight, Download, Terminal, Cloud, Laptop, Check, Code2, Keyboard, History, ShieldCheck } from 'lucide-react';
import Demo from '../components/Demo';
import { agents } from '../components/agents';
import { SiteHeader, SiteFooter, repo } from '../components/SiteChrome';

const download = '/download/fennec/latest';
function Start() {
  return <a className="button primary" href={download}><Download size={17}/>Download for macOS</a>;
}

export default function Home() {
  return <>
    <SiteHeader cta={{ label: 'Download', href: download }}/>
    <main>
      <section className="hero">
        <h1>Cloud agents. <span>Local tools.</span></h1>
        <p>Your coding agents, running in the cloud. Connected to your Mac. <br/>Close your laptop. Come back to the same work.</p>
        <div className="hero-actions"><Start/><a className="button secondary" href="#demo"><Terminal size={18}/>See how it works</a></div>
        <span className="platform-note">For macOS · Apple Silicon · Developer preview</span>
      </section>

      <section className="supported-agents" aria-labelledby="supported-agents-title">
        <h2 id="supported-agents-title">Choose your coding agent.</h2>
        <ul>{agents.map(a => <li key={a.cmd}><img src={'/icons/' + a.icon} width="24" height="24" alt=""/>{a.name}</li>)}</ul>
      </section>
      <div className="demo-wrap"><Demo/></div>

      <section className="intro-section" id="how-it-works">
        <h2>Keep your favorite tools.<br/><span>Give your agents more freedom.</span></h2>
        <p>Keep tmux, cmux, Herdr, your editor, and your way of working.<br/>Fennec gives the long-running work a place to stay.</p>
        <div className="feature-grid">
          <article><Terminal size={22} strokeWidth={1.5}/><h3>Start as you always do.</h3><p>Choose Cloud computer in Fennec, then run your agent from your terminal. Your local directory provides the starting context.</p></article>
          <article><Cloud size={22} strokeWidth={1.5}/><h3>Let the cloud keep going.</h3><p>Agent sessions stay on your cloud computer. Closing a terminal or your Mac doesn’t stop cloud-independent work.</p></article>
          <article><Laptop size={22} strokeWidth={1.5}/><h3>Pick up the same thread.</h3><p>Attach from your terminal or open the session in a browser. If the connection drops, Fennec automatically tries to reconnect to your running session.</p></article>
          <article><History size={22} strokeWidth={1.5}/><h3>Bring your conversation.</h3><p>Continue a conversation you started on your Mac using your agent’s supported resume command. Fennec brings its history into the cloud session.</p></article>
          <article><Keyboard size={22} strokeWidth={1.5}/><h3>Keep typing. Paste a screenshot.</h3><p>Local input prediction makes supported terminal input feel responsive, including Chinese text. Paste clipboard images into supported cloud agent terminals.</p></article>
          <article><ShieldCheck size={22} strokeWidth={1.5}/><h3>You choose the access.</h3><p>Choose Ask for approval, Approve for me, or Full access. Local files and tools stay behind the permissions you set.</p></article>
        </div>
      </section>

      <section className="team-section">
        <div className="team-copy"><h2>Your agents.<br/>Room to keep working.</h2><p>Code in one session. Review in another. One shared cloud computer, with your sessions just a click away.</p><a className="text-link" href="#demo">Try the session preview <ArrowUpRight size={16}/></a></div>
        <div className="team-board">
          <div className="board-label"><span>Your cloud sessions</span><span>3 sample sessions</span></div>
          {[
            { name: 'Codex', icon: 'codex-color.svg', task: 'Shipping the next feature.', state: 'Ready for review' },
            { name: 'Claude Code', icon: 'claudecode-color.svg', task: 'Working through the details.', state: 'Tests passed' },
            { name: 'OpenCode', icon: 'opencode.svg', task: 'Exploring another approach.', state: 'Waiting for you' },
          ].map(a => <a key={a.name} href="#demo" className="team-card"><img src={'/icons/' + a.icon} width="40" height="40" alt=""/><div><strong>{a.name}</strong><p>{a.task}</p></div><span><Check size={11}/>{a.state}</span></a>)}
          <div className="board-foot"><Cloud size={14}/>Your agents share a cloud computer. Each session keeps its place.</div>
        </div>
      </section>

      <section className="open-section">
        <div className="open-icon"><Code2 size={30} strokeWidth={1.4}/></div>
        <div><div className="eyebrow">BRAIN IN THE CLOUD. HANDS ON YOUR MAC.</div><h2>Local files, tools,<br/>and a helping hand.</h2><p>Your agent can reach local files and tools through the access you approve.<br/>Prepare a cloud checkout to keep working when your Mac is offline.</p><p className="computer-use-note">Need your agent to operate Mac apps and web pages? Fennec Computer Use is a separate app that works on its own or alongside Fennec. Connect Claude Code or Codex from its setup window.</p><div className="open-links"><a className="text-link" href="/computer-use/">Explore Fennec Computer Use <ArrowUpRight size={15}/></a><a className="text-link" href="#get-started">Meet the fen CLI <ArrowUpRight size={15}/></a></div></div>
      </section>

      <section className="faq-section" id="faq"><h2>A few things to know.</h2><div>{[
        ['What is Fennec?', 'Fennec is a macOS companion and CLI for running coding agents in persistent cloud sessions. Manage sessions from the menu bar, attach in your terminal, or open a browser terminal.'],
        ['Which agents does it support?', 'Cloud execution is available for ' + agents.map(a => a.name).join(', ') + '. Other discovered agents are marked Coming soon in the app. Cursor CLI uses cursor-agent; your Cursor editor command stays unchanged.'],
        ['Can I keep tmux, cmux, and Herdr?', 'Yes. Keep your terminal workflow and launch your agent as usual. Fennec also bridges supported cmux hooks for cloud Codex sessions back to your Mac, with your approval. This does not include the separate cmux-cua MCP integration.'],
        ['Can I use my existing model account?', 'Yes. Fennec synchronizes supported native agent credentials into isolated cloud configuration. Magpie is optional for model routing and selection. Cursor CLI uses its native account. For OpenCode 2 accounts, use Magpie: credentials stored only in its database are not yet synchronized.'],
        ['What happens when I close my Mac?', 'The cloud session keeps running while its cloud computer is available. Work that already has its files and dependencies in the cloud can continue. Steps that need your Mac wait until it reconnects.'],
        ['Are my files automatically mirrored?', 'No. Your starting directory provides context; it is not permission to copy your entire home folder. The agent selects task-relevant files, works in a cloud checkout, and reconciles changes back to your Mac.'],
        ['What do I need to get started?', 'An Apple Silicon Mac, a Runta account, and supported model access. Download the Fennec app, sign in, and choose Cloud computer for your agent. Fennec is in developer preview.'],
        ['Does the website connect to a real agent?', 'No. The preview is an interactive illustration with sample sessions. It does not run commands, connect to your computer, or send a task to an AI service.'],
        ['Are cloud and model usage included?', 'Cloud compute and model usage depend on your Runta account and model provider. They are separate from the macOS companion.'],
      ].map(([q, a]) => <details key={q}><summary>{q}<span className="faq-plus" aria-hidden="true">+</span></summary><p>{a}</p></details>)}</div></section>

      <section className="getting-started" id="get-started">
        <div><div className="eyebrow">MEET FEN</div><h2>A short command.<br/>A long-running session.</h2><p>Once Fennec is installed and signed in, choose an agent to run in the cloud. Press Ctrl+C to detach without stopping its work. Run fen to choose a session when you return.</p><span className="preview-note">Developer preview. <a className="text-link-inline" href={repo + '#get-started'}>Setup instructions</a> and <a className="text-link-inline" href={repo + '/releases?q=v0.&expanded=true'}>release notes</a> are available on GitHub.</span></div>
        <div className="cli-card"><code><span>›</span> codex <small>Start a cloud session</small><br/><span>›</span> fen <small>Choose a session</small><br/><span>›</span> fen attach sunny-spicy <small>Pick up where you left off</small><br/><span>›</span> fen stop sunny-spicy <small>Stop when you’re done</small></code></div>
      </section>
      <section className="final-cta"><img src="/fennec.svg" width="72" height="72" alt="Fennec"/><h2>Step away. Keep going.</h2><p>Your work will be right where you left it.</p><Start/><span className="platform-note">macOS · Apple Silicon</span></section>
    </main>
    <SiteFooter/>
  </>;
}

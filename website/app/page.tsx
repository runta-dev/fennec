import { ArrowRight, ArrowUpRight, Download, Github, Terminal, Cloud, Laptop, Check, ShieldCheck, Smartphone } from 'lucide-react';
import Demo from '../components/Demo';
import MobileShowcase from '../components/MobileShowcase';
import { agents } from '../components/agents';
import { SiteHeader, SiteFooter, repo } from '../components/SiteChrome';
import './landing.css';

const download = '/download/fennec/latest';
function Start() {
  return <a className="button primary" href={download}><Download size={17}/>Download for macOS</a>;
}

export default function Home() {
  return <>
    <SiteHeader cta={{ label: 'Download', href: download }}/>
    <main className="fennec-landing">
      <section className="hero">
        <h1>Cloud agents. <span>Local tools.</span></h1>
        <p>Your coding agents, running in the cloud. Connected to your Mac. <br/>Close your laptop. Come back to the same work.</p>
        <div className="hero-actions"><Start/><a className="button secondary" href={repo}><Github size={18}/>GitHub</a></div>
        <span className="platform-note">For macOS · Apple Silicon · Developer preview</span>
      </section>

      <div className="demo-wrap"><Demo/></div>
      <section className="supported-agents" aria-labelledby="supported-agents-title">
        <h2 id="supported-agents-title">Your agent. Your familiar workflow.</h2>
        <ul>{agents.map(a => <li key={a.cmd}><img src={'/icons/' + a.icon} width="24" height="24" alt=""/>{a.name}</li>)}</ul>
        <p className="terminal-tools">Keep your terminal, <a href="https://github.com/tmux/tmux/wiki" target="_blank" rel="noopener noreferrer"><img src="/icons/tmux.svg" width="18" height="18" alt=""/>tmux</a>, <a href="https://cmux.com/" target="_blank" rel="noopener noreferrer"><img src="/icons/cmux.png" width="20" height="20" alt=""/>cmux</a>, <a href="https://herdr.dev/" target="_blank" rel="noopener noreferrer"><img src="/icons/herdr.png" width="20" height="20" alt=""/>Herdr</a>, and your editor.</p>
      </section>

      <section className="landing-benefits" id="how-it-works" aria-labelledby="benefits-title">
        <div className="landing-section-heading"><div className="eyebrow">HOW IT WORKS</div><h2 id="benefits-title">Run in the cloud.<br/><span>Keep your way of working.</span></h2></div>
        <article className="benefit-row">
          <div className="benefit-copy"><span className="benefit-number">01 / KEEP GOING</span><h3>Your work keeps running.</h3><p>Give the task a cloud checkout with the files and dependencies it needs. Your agent keeps working there when you close your terminal or your Mac.</p><p className="benefit-detail">A cloud session continues while its computer is running. Any step that needs your Mac waits for it to reconnect.</p></div>
          <figure className="benefit-proof cloud-proof"><div className="proof-topline"><Cloud size={16}/><span>Execution stays in the cloud</span></div><div className="cloud-proof-flow"><span>Project files</span><ArrowRight size={16}/><span><img src="/icons/codex-color.svg" width="19" height="19" alt=""/>Your agent</span><ArrowRight size={16}/><span>Tests &amp; results</span></div><div className="cloud-proof-local"><Laptop size={16}/><span>Your Mac’s files &amp; tools</span><span>Available when connected</span></div></figure>
        </article>
        <article className="benefit-row">
          <div className="benefit-copy"><span className="benefit-number">02 / PICK UP THE THREAD</span><h3>Same session. Same context.</h3><p>Detach without stopping the work. Come back through your terminal or a browser terminal, with the session’s conversation and results still in place.</p><p className="benefit-detail">Run <code>fen</code> to choose a live session. No session ID to memorize.</p></div>
          <figure className="benefit-proof resume-proof"><div className="proof-topline"><Terminal size={16}/><span>A short command to come back</span></div><code><span>›</span> fen</code><div className="resume-session"><img src="/icons/codex-color.svg" width="22" height="22" alt=""/><div><strong>Fix duplicate webhook deliveries</strong><span>Codex · maple-river</span></div><ArrowRight size={16}/></div></figure>
        </article>
        <article className="benefit-row">
          <div className="benefit-copy"><span className="benefit-number">03 / STAY IN CONTROL</span><h3>Your Mac’s tools.<br/>Your permission.</h3><p>Your cloud agent can reach local files and tools through the access you choose. Set Ask for approval, Approve for me, or Full access in Fennec.</p><p className="benefit-detail">Local access needs your Mac to be online. You can review activity and manage permissions from the app.</p></div>
          <figure className="benefit-proof permission-proof"><div className="proof-topline"><ShieldCheck size={16}/><span>Example: Ask for approval</span></div><ol><li><span>1</span><div><strong>Agent requests local access</strong><small>Read the project’s files on your Mac</small></div></li><li><span>2</span><div><strong>You review the request</strong><small>Approve or decline before it runs</small></div></li><li><Check size={16}/><div><strong>Approved access can proceed</strong><small>Your Mac provides the local tool</small></div></li></ol></figure>
        </article>
      </section>

      <section className="landing-mobile" id="mobile" aria-labelledby="mobile-title">
        <div className="mobile-copy">
          <div className="landing-section-heading"><div className="eyebrow">ON YOUR IPHONE</div><h2 id="mobile-title">Your sessions.<br/><span>Wherever you are.</span></h2></div>
          <p>Fennec for iPhone connects to the same cloud computer as your Mac. Check on your agents away from your desk, and keep them moving.</p>
          <ul className="mobile-features">
            <li><Check size={16}/><div><strong>Every session at a glance</strong><span>Grouped by Working and Idle, as on your Mac.</span></div></li>
            <li><Check size={16}/><div><strong>Follow the work</strong><span>The agent’s replies, the commands it ran, and how long it worked.</span></div></li>
            <li><Check size={16}/><div><strong>Reply, stop, or start</strong><span>Send a follow-up, stop a session, open its terminal, or start a new one.</span></div></li>
          </ul>
          <p className="benefit-detail"><Smartphone size={14}/>To pair, open the Fennec menu bar app, choose Get Fennec iOS App, and scan the code.</p>
        </div>
        <figure className="mobile-stage" aria-label="Fennec for iPhone, replaying the sessions from the walkthrough above"><MobileShowcase/></figure>
      </section>

      <section className="landing-setup" id="get-started" aria-labelledby="setup-title">
        <div className="landing-section-heading"><div className="eyebrow">GET STARTED</div><h2 id="setup-title">From your Mac<br/><span>to your first cloud session.</span></h2></div>
        <div className="setup-grid">
          <ol className="setup-steps">
            <li><span>1</span><div><h3>Download and sign in.</h3><p>Install Fennec on an Apple Silicon Mac, open it from the menu bar, and sign in with your Runta account.</p></div></li>
            <li><span>2</span><div><h3>Choose your agent.</h3><p>Select Cloud computer. Use supported credentials from your existing model account; Magpie is optional for model routing.</p></div></li>
            <li><span>3</span><div><h3>Start from your terminal.</h3><p>Run your usual agent command, such as <code>codex</code>. Have the agent prepare the project in the cloud before you step away.</p></div></li>
          </ol>
          <div className="setup-costs"><h3>What you need. What you pay for.</h3><p>Fennec is in developer preview. A Runta account and supported model access are required, or <a href="/self-hosted/">your own server</a> in place of the Runta account.</p><dl><div><dt>Cloud compute</dt><dd>Billed through your Runta account, or by your own server provider.</dd></div><div><dt>Model usage</dt><dd>Covered by your model provider’s plan or usage billing, separately from cloud compute.</dd></div></dl><p className="costs-note">Downloading the macOS companion does not include cloud compute or model usage.</p><a className="text-link" href={repo + '#get-started'}>Read the setup guide <ArrowUpRight size={15}/></a></div>
        </div>
        <div className="setup-actions"><Start/><a className="text-link" href={repo + '/releases?q=v0.&expanded=true'}>Release notes <ArrowUpRight size={15}/></a></div>
      </section>

      <aside className="computer-use-aside"><div><strong>Prefer to run the cloud computer yourself?</strong><p>Pair Fennec with a Linux server you control, in one command. No Runta account needed.</p></div><a className="text-link" href="/self-hosted/">Use your own server <ArrowUpRight size={15}/></a></aside>

      <aside className="computer-use-aside"><div><strong>Need your agent to operate Mac apps and web pages?</strong><p>Fennec Computer Use is a separate app. Use it on its own or alongside Fennec.</p></div><a className="text-link" href="/computer-use/">Explore Computer Use <ArrowUpRight size={15}/></a></aside>

      <section className="faq-section" id="faq"><h2>A few things to know.</h2><div>{[
        ['Which agents does it support?', 'Cloud execution is available for ' + agents.map(a => a.name).join(', ') + '. Other discovered agents are marked Coming soon in the app. Cursor CLI uses cursor-agent; your Cursor editor command stays unchanged.'],
        ['Can I use my existing model account?', 'Yes. Fennec synchronizes supported native agent credentials into isolated cloud configuration. Magpie is optional for model routing and selection. Cursor CLI uses its native account. For OpenCode 2 accounts, use Magpie: credentials stored only in its database are not yet synchronized.'],
        ['What happens when I close my Mac?', 'The cloud session keeps running while its cloud computer is available. Work that already has its files and dependencies in the cloud can continue. Steps that need your Mac wait until it reconnects.'],
        ['Are my files automatically mirrored?', 'No. Your starting directory provides context; it is not permission to copy your entire home folder. The agent selects task-relevant files, works in a cloud checkout, and reconciles changes back to your Mac.'],
        ['Can I bring an existing conversation?', 'Yes, using your agent’s supported resume command. Fennec brings the local conversation history into the cloud session so you can continue there.'],
        ['Can I keep my terminal tools and shortcuts?', 'Yes. Keep tmux, cmux, Herdr, and your terminal workflow. Fennec supports clipboard image paste in supported cloud agent terminals, input prediction for supported terminal input, and approved cmux hooks for cloud Codex sessions.'],
        ['Does this preview run a real agent?', 'No. The app preview uses browser-only sample data and prerecorded terminal output. It does not connect to your computer or send tasks to an AI service.'],
        ['Can I use my own server?', 'Yes. Fennec can pair with a Linux server you run with Docker instead of a Runta cloud computer, with no Runta account. Your Mac connects to it directly over SSH. See Use your own server.'],
        ['Are cloud and model usage included?', 'No. Cloud compute is billed through your Runta account, and model usage depends on your provider’s plan or usage billing. They are separate from the macOS companion.'],
      ].map(([q, a]) => <details key={q}><summary>{q}<span className="faq-plus" aria-hidden="true">+</span></summary><p>{a}</p></details>)}</div></section>

      <section className="final-cta"><img src="/fennec.svg" width="72" height="72" alt="Fennec"/><h2>Step away. Keep going.</h2><p>Come back to the same session.</p><Start/><span className="platform-note">macOS · Apple Silicon · Developer preview</span><a className="final-setup-link" href="#get-started">Account requirements &amp; costs</a></section>
    </main>
    <SiteFooter/>
  </>;
}

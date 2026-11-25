import { ArrowUpRight, Download, KeyRound, Server, Terminal, WifiOff } from 'lucide-react';
import { SiteHeader, SiteFooter, repo, fennecBrand } from '../../components/SiteChrome';

export const metadata = {
  alternates: { canonical: '/self-hosted/' },
  title: 'Fennec on your own server — Run cloud agents on a machine you control.',
  description: 'Run Fennec cloud agents on your own Linux server with Docker. No Runta account needed: your Mac connects straight to the server over SSH.',
  openGraph: { url: 'https://fennec.runta.com/self-hosted/', title: 'Fennec on your own server', description: 'Run cloud agents on a Linux server you control, paired with your Mac in one command.', type: 'website', siteName: 'Fennec', images: [{ url: '/fennec.svg', alt: 'Fennec' }] },
};

// Self-hosting is part of Fennec, not a separate app: the same header,
// download and FAQ conventions as the homepage.
const download = '/download/fennec/latest';
const guide = repo + '/blob/main/deploy/byoc/README.md';
const image = 'docker.io/shiqimei/fennec-cloud:1';

export default function SelfHosted() {
  return <>
    <SiteHeader brand={fennecBrand} cta={{ label: 'Download', href: download }}/>
    <main>
      <section className="hero cu-hero">
        <img className="cu-mark" src="/fennec.svg" width="88" height="88" alt=""/>
        <h1>Fennec <span>on your own server</span></h1>
        <p className="cu-tagline">Your machine. Your cloud agents.</p>
        <p>Run Fennec&apos;s cloud computer on a Linux server you control. <br/>Your agents keep working there while your Mac comes and goes.</p>
        <div className="hero-actions"><a className="button primary" href="#deploy"><Server size={17}/>Deploy in one command</a><a className="button secondary" href={download}><Download size={17}/>Download Fennec</a></div>
        <span className="platform-note">Linux with Docker · amd64 or arm64 · Fennec on an Apple Silicon Mac</span>
      </section>

      <section className="intro-section cu-intro">
        <h2>No account to make.<br/><span>Nothing in between.</span></h2>
        <p>Your Mac talks to your server directly over SSH. Fennec prepares it the first time, then your agents launch there as they would on a Runta cloud computer.</p>
        <div className="feature-grid">
          <article><KeyRound size={22} strokeWidth={1.5}/><h3>Pinned, not trusted on first sight.</h3><p>Pairing reads the server&apos;s key over your own SSH connection. Every later connection checks that key and the machine&apos;s identity before anything runs.</p></article>
          <article><Terminal size={22} strokeWidth={1.5}/><h3>The same Fennec.</h3><p>Launch from your terminal, detach with Ctrl+C, attach again with <code>fen</code>. Approved access to your Mac works the same way.</p></article>
          <article><WifiOff size={22} strokeWidth={1.5}/><h3>Says when it can&apos;t reach it.</h3><p>If the server goes away, Fennec shows that it can&apos;t reach it and reconnects by itself when it is back.</p></article>
        </div>
      </section>

      <section className="getting-started" id="deploy">
        <div><div className="eyebrow">DEPLOY</div><h2>One command.<br/>From your Mac.</h2><p>If you can already <code>ssh</code> to the server and it has Docker, Fennec starts its image there, reads the server&apos;s key back over that connection, and pairs this Mac with it.</p><a className="text-link" href={guide}>Deploy by hand, options and details <ArrowUpRight size={15}/></a></div>
        <div className="cli-card cu-steps">
          <ol>
            <li><b>Install Fennec on your Mac.</b><small>Download it, open it from Applications. No sign-in is needed for your own server.</small></li>
            <li><b>Deploy and pair.</b><small><code>fen cloud byoc deploy you@your-server</code> starts <code>{image}</code> on port 2222 and pairs.</small></li>
            <li><b>Launch your agent.</b><small>Set its run location to Cloud computer and run it as usual, for example <code>codex</code>.</small></li>
          </ol>
        </div>
      </section>

      <section className="faq-section" id="faq"><h2>A few things to know.</h2><div>{[
        ['Do I need a Runta account?', 'No. A paired server replaces the Runta cloud computer and account. Account, billing and iPhone pairing need a Runta account and are hidden while a server is paired; unpair to sign in to Runta.'],
        ['What does the server need?', 'Linux on amd64 or arm64 with Docker, SSH access from your Mac, and one port your Mac can reach. A VPN or private network works: use --address when your Mac reaches the server at a different address than SSH does.'],
        ['Where do my agents run?', 'In a container from the Fennec cloud image. The container is the boundary: agents have sudo inside it. Do not mount the Docker socket or host folders you do not want agents to change.'],
        ['What survives a restart?', 'Everything in the home volume: agents, credentials Fennec synchronized, and session files. Restarting the container ends agents that were running, as restarting a machine would.'],
        ['How do I stop using it?', 'Run fen cloud byoc unpair with the machine ID shown by fen cloud byoc status. Removing the container and its volume on the server deletes everything Fennec put there.'],
        ['Who pays for what?', 'You pay your server provider for the machine and your model provider for model usage. Fennec itself needs no Runta billing for a self-hosted server.'],
      ].map(([q, a]) => <details key={q}><summary>{q}<span className="faq-plus" aria-hidden="true">+</span></summary><p>{a}</p></details>)}</div></section>

      <section className="final-cta"><img src="/fennec.svg" width="72" height="72" alt="Fennec"/><h2>Your server. Your agents.</h2><p>One command from your Mac, and they keep working while you step away.</p><a className="button primary" href="#deploy"><Server size={17}/>Deploy in one command</a></section>
    </main>
    <SiteFooter/>
  </>;
}

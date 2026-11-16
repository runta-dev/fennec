import { ArrowDown, ArrowUpRight } from 'lucide-react';
export const runta = 'https://runta.com/';
export const repo = 'https://github.com/runta-dev/fennec';

// Links are absolute ("/#faq") so they work from every page.
export function SiteHeader({ cta = { label: 'Get started', href: repo + '#get-started' } }: { cta?: { label: string; href: string } }) {
  return <header className="site-header"><a className="wordmark" href="/" aria-label="Fennec home"><img src="/fennec.svg" width="34" height="34" alt=""/>Fennec</a><nav aria-label="Main navigation"><a href="/#how-it-works">How it works</a><a href="/computer-use/">Computer Use</a><a href="/#faq">FAQ</a><a href={repo}>GitHub <ArrowUpRight size={12}/></a></nav><div className="header-actions"><a className="nav-download" href={cta.href}>{cta.label} <ArrowDown size={14}/></a></div></header>;
}

export function SiteFooter({ tagline = 'Cloud agents. Local tools.' }: { tagline?: string }) {
  return <footer><a href="/" className="wordmark"><img src="/fennec.svg" width="28" height="28" alt=""/>Fennec</a><div><a href="/#how-it-works">How it works</a><a href="/computer-use/">Computer Use</a><a href="/#faq">FAQ</a><a href={repo}>GitHub</a><a href={runta}>Runta</a></div><small>© {new Date().getFullYear()} Runta</small><p className="footer-attribution">{tagline} Built by <a href={runta}>Runta</a>.</p></footer>;
}

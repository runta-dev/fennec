import { ArrowDown, ArrowUpRight } from 'lucide-react';
export const runta = 'https://runta.com/';
export const repo = 'https://github.com/runta-dev/fennec';

// A product the header and footer present: Fennec, or a sub-product such as
// Fennec Computer Use, with its own mark, home and links.
export type Brand = { name: string; sub?: string; logo: string; home: string; nav: { label: string; href: string }[] };

// Links are absolute ("/#faq") so they work from every page.
export const fennecBrand: Brand = {
  name: 'Fennec', logo: '/fennec.svg', home: '/',
  nav: [{ label: 'How it works', href: '/#demo' }, { label: 'Computer Use', href: '/computer-use/' }, { label: 'Your server', href: '/self-hosted/' }, { label: 'FAQ', href: '/#faq' }],
};

function Wordmark({ brand, size }: { brand: Brand; size: number }) {
  return <a className="wordmark" href={brand.home} aria-label={(brand.sub ? brand.name + ' ' + brand.sub : brand.name) + ' home'}><img src={brand.logo} width={size} height={size} alt=""/>{brand.name}{brand.sub && <span className="wordmark-sub">{brand.sub}</span>}</a>;
}

export function SiteHeader({ brand = fennecBrand, cta = { label: 'Get started', href: repo + '#get-started' } }: { brand?: Brand; cta?: { label: string; href: string } }) {
  return <header className={'site-header' + (brand === fennecBrand ? ' fennec-header' : '')}><Wordmark brand={brand} size={34}/><nav aria-label="Main navigation">{brand.nav.map(l => <a key={l.href} href={l.href}>{l.label}</a>)}<a href={repo}>GitHub <ArrowUpRight size={12}/></a></nav><div className="header-actions"><a className="nav-download" href={cta.href}>{cta.label} <ArrowDown size={14}/></a></div></header>;
}

export function SiteFooter({ brand = fennecBrand, tagline = 'Cloud agents. Local tools.' }: { brand?: Brand; tagline?: string }) {
  return <footer><Wordmark brand={brand} size={28}/><div>{brand.nav.map(l => <a key={l.href} href={l.href}>{l.label}</a>)}<a href={repo}>GitHub</a><a href={runta}>Runta</a><a href="/privacy/">Privacy</a></div><small>© {new Date().getFullYear()} Runta</small><p className="footer-attribution">{tagline} Built by <a href={runta}>Runta</a>.</p></footer>;
}

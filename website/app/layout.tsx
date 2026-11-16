import type { ReactNode } from 'react';
import './reference-design.css';
import './globals.css';
import './fennec.css';
export const metadata = {
 metadataBase: new URL('https://fennec.runta.com'),
 alternates: { canonical: '/' },
 title: 'Fennec — Cloud agents. Local tools.',
 description: 'Run Codex, Claude Code, Pi, Kimi Code, Cursor CLI, Grok Build, and OpenCode in persistent cloud sessions. Keep tmux, cmux, Herdr, and your Mac tools.',
 icons: { icon: '/fennec.svg' },
 openGraph: { url: 'https://fennec.runta.com/', title: 'Fennec — Cloud agents. Local tools.', description: 'Seven coding harnesses. Keep tmux, cmux, Herdr, and your Mac tools. Persistent cloud sessions that stay with you.', type: 'website', siteName: 'Fennec' },
 twitter: { card: 'summary', title: 'Fennec — Cloud agents. Local tools.' },
};
export default function Layout({children}:{children:ReactNode}) { return <html lang="en"><body>{children}</body></html>; }

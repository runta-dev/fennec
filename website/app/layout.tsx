import type { ReactNode } from 'react';
import './reference-design.css';
import './globals.css';
import './fennec.css';
export const metadata = {
 metadataBase: new URL('https://fennec.runta.com'),
 alternates: { canonical: '/' },
 title: 'Fennec — Cloud agents. Local tools.',
 description: 'Run your coding agents in persistent cloud sessions, connected to your Mac. Close your laptop. Come back to the same work.',
 icons: { icon: '/fennec.svg' },
 openGraph: { url: 'https://fennec.runta.com/', title: 'Fennec — Cloud agents. Local tools.', description: 'Your terminal, without the tether. Persistent cloud agents connected to your Mac.', type: 'website', siteName: 'Fennec' },
 twitter: { card: 'summary', title: 'Fennec — Cloud agents. Local tools.' },
};
export default function Layout({children}:{children:ReactNode}) { return <html lang="en"><body>{children}</body></html>; }

import type { ReactNode } from 'react';
import './reference-design.css';
import './globals.css';
import './fennec.css';
export const metadata = {
 metadataBase: new URL('https://fennec.runta.com'),
 alternates: { canonical: '/' },
 title: 'Fennec — Cloud agents. Local tools.',
 description: 'Your coding agents, running in the cloud. Connected to your Mac.',
 icons: { icon: '/fennec.svg', apple: '/og-image.png' },
 openGraph: { url: 'https://fennec.runta.com/', title: 'Fennec — Cloud agents. Local tools.', description: 'Your coding agents, running in the cloud. Connected to your Mac.', type: 'website', siteName: 'Fennec', images: [{ url: '/og-image.png', width: 1024, height: 1024, alt: 'Fennec' }] },
 twitter: { card: 'summary', title: 'Fennec — Cloud agents. Local tools.', description: 'Your coding agents, running in the cloud. Connected to your Mac.', images: ['/og-image.png'] },
};
export default function Layout({children}:{children:ReactNode}) { return <html lang="en"><body>{children}</body></html>; }

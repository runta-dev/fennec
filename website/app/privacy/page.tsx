import type { ReactNode } from 'react';
import { SiteHeader, SiteFooter } from '../../components/SiteChrome';

export const metadata = {
  alternates: { canonical: '/privacy/' },
  title: 'Privacy Policy — Fennec',
  description: 'What Fennec for Mac, Fennec for iPhone and the Fennec cloud computer collect, why, who else handles it, and how to delete it.',
  openGraph: { url: 'https://fennec.runta.com/privacy/', title: 'Fennec Privacy Policy', description: 'What Fennec collects, why, and how to delete it.', type: 'website', siteName: 'Fennec', images: [{ url: '/fennec.svg', alt: 'Fennec' }] },
};

// Keep this page true to the code: ios/Fennec (camera, Keychain, UserDefaults,
// notifications), docs/native-agent-credentials.md (credential sync) and
// ios/Fennec/PrivacyInfo.xcprivacy. It supplements the Runta Privacy Notice
// (runtaNotice): keep the operator, contact, named providers, retention,
// changes and transfers wording in step with it. Change the date when the text changes.
const updated = 'November 25, 2026';
const contact = 'support@runta.com';
const runtaNotice = 'https://runta.com/privacy-policy/';

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return <section id={id}><h2>{title}</h2>{children}</section>;
}

export default function Privacy() {
  return <>
    <SiteHeader/>
    <main className="legal">
      <h1>Privacy Policy</h1>
      <p className="legal-updated">Last updated {updated}</p>

      <p>Fennec is made by Runta Inc. (&ldquo;Runta&rdquo;, &ldquo;we&rdquo;, &ldquo;us&rdquo;). This policy covers Fennec for Mac, Fennec for iPhone, the Fennec cloud computer that Runta runs for your account, and this website. It explains what we collect, why, who else handles it, and how to delete it.</p>
      <p>Fennec is part of Runta&apos;s services, so the <a href={runtaNotice}>Runta Privacy Notice</a> also applies to it. This page adds what is specific to Fennec.</p>
      <p>The short version: we collect what we need to sign you in and run your agents. We don&apos;t sell your data, we don&apos;t show ads, and the apps contain no third-party analytics or tracking.</p>

      <Section id="account" title="Your Runta account">
        <p>Fennec uses a Runta account. When you sign in we receive:</p>
        <ul>
          <li><b>Email address.</b> With email sign-in we send a one-time code to it. Email sign-in is protected by Cloudflare Turnstile, which checks that the request comes from a person.</li>
          <li><b>Profile from Google or GitHub.</b> If you continue with Google or GitHub, we receive your name, email address, profile picture and account ID from that provider. We never see your Google or GitHub password.</li>
          <li><b>Device keys.</b> Each signed-in device gets its own API key, named after the device type and a random four-letter ID, for example &ldquo;Fennec iPhone (ab3k)&rdquo;. It lets you see and revoke each device.</li>
          <li><b>Billing.</b> Your plan, credit balance and cloud computer usage. Stripe processes card and bank details, and Metronome processes usage billing; we receive only the billing and transaction information needed to run your account.</li>
        </ul>
      </Section>

      <Section id="sessions" title="Sessions, files and agent credentials">
        <p>Fennec runs coding agents (such as Claude Code, Codex and Pi) on a cloud computer that belongs to your account. To do that, the cloud computer stores and processes:</p>
        <ul>
          <li><b>Session content:</b> your prompts and messages, agent replies, terminal output, and the files you or your agents create or change there.</li>
          <li><b>Attachments:</b> photos and files you choose to send to a session. The app only reads the items you pick.</li>
          <li><b>Agent sign-ins:</b> Fennec for Mac copies the sign-in and model settings of the agents you use on your Mac to an isolated, private folder on your cloud computer, so those agents can run there under your own accounts.</li>
          <li><b>Access to your Mac:</b> when an agent in the cloud needs a file or command on your Mac, Fennec asks for your approval, depending on your Governance setting. Only what you approve is sent.</li>
        </ul>
        <p>We use this content only to provide the service to you: running, showing and resuming your sessions on your devices. We don&apos;t use it for advertising or sell it, and we don&apos;t use it to train AI models. Fennec does not turn on Runta&apos;s optional LLM tool capture (Token Analyze) for your cloud computer.</p>
        <p>The agents themselves send your prompts and code to the model provider you chose (for example Anthropic or OpenAI), under your account with that provider. That provider&apos;s privacy policy applies to what it receives.</p>
        <p>If you pair Fennec with your own server instead of a Runta cloud computer, sessions, files and agent sign-ins stay on that server, and you don&apos;t need a Runta account.</p>
      </Section>

      <Section id="iphone" title="On your iPhone">
        <ul>
          <li><b>Camera.</b> Used only to scan the sign-in QR code that Fennec for Mac shows. Camera frames are read on the iPhone to find the code; they aren&apos;t saved or uploaded. Instead of the camera, you can choose a screenshot of the code from Photos.</li>
          <li><b>Photos and files.</b> Only the items you choose in the system picker, to scan a code or attach to a session.</li>
          <li><b>Notifications.</b> Optional. If you allow them, Fennec uses them to tell you when a session needs your approval, has a question or has finished. Your notification choices are kept on the iPhone.</li>
          <li><b>Stored on the device.</b> Your sign-in token and the device&apos;s random ID are kept in the iPhone Keychain. Settings such as appearance, language, notification choices and unsent drafts are kept in the app&apos;s own storage. Signing out removes the token.</li>
        </ul>
        <p>The App Review demo (appreview@runta.com) runs entirely on the iPhone with sample sessions and sends no session content to us.</p>
      </Section>

      <Section id="mac" title="On your Mac">
        <p>Fennec for Mac keeps its settings, session list and logs on your Mac. It reads your agents&apos; local sign-ins only to copy them to your cloud computer as described above, and only for the agents Fennec supports in the cloud.</p>
      </Section>

      <Section id="technical" title="Technical and website data">
        <p>Like most online services, our servers and Cloudflare, which hosts this website and app downloads, log technical data such as IP addresses, request times, app version and errors. We use it to run, secure and fix the service. This website sets no cookies of its own and no advertising or analytics cookies. Fennec doesn&apos;t respond to browser Do Not Track signals, because it doesn&apos;t track you.</p>
        <p>If you install a beta through TestFlight and choose to send feedback or crash reports, Apple shares them with us.</p>
      </Section>

      <Section id="sharing" title="Who else handles your data">
        <p>We share data only with service providers that help us run Fennec, and only for that purpose:</p>
        <ul>
          <li>Supabase, for sign-in and account data.</li>
          <li>Google Cloud and object-storage providers, which host your cloud computer, our databases, logs and backups.</li>
          <li>Cloudflare, for this website, downloads, Turnstile and security.</li>
          <li>Google and GitHub, if you sign in with them.</li>
          <li>Resend, to send sign-in codes and other email.</li>
          <li>Stripe for payments and Metronome for usage billing.</li>
          <li>The AI providers you choose, such as Anthropic or OpenAI, when your agents send them requests.</li>
        </ul>
        <p>We may also disclose data to professional advisers, auditors and insurers; to law enforcement, regulators or courts when required by law or reasonably necessary to protect rights, safety and security; and to a successor in a merger, acquisition, financing, reorganization or sale of assets. We don&apos;t sell personal information or share it for cross-context behavioral advertising.</p>
      </Section>

      <Section id="retention" title="Keeping and deleting your data">
        <p>We keep your account information and cloud computer data while your account is active. Billing and transaction records are kept as the law requires. We may keep information longer to resolve disputes, prevent fraud or abuse, comply with law, or in limited backups; when we no longer need it, we delete or deidentify it. You can stop sessions and delete files on your cloud computer at any time, and sign out to remove your sign-in from a device.</p>
        <p>To delete your Runta account, email <a href={'mailto:' + contact}>{contact}</a> from the address on the account. We then delete the account, its cloud computer and its session data, except what we must keep as described above.</p>
        <p>Depending on where you live, you may also ask us for access to your data, a copy of it, or a correction, at the same address. We may need to verify your identity before completing a request.</p>
      </Section>

      <Section id="security" title="Security">
        <p>Connections between your devices, our servers and your cloud computer are encrypted. Sign-in tokens are kept in the Keychain on Apple devices, and agent sign-ins on your cloud computer are stored in private files only your cloud computer&apos;s user can read. No security measures are perfect, so please tell us at <a href={'mailto:' + contact}>{contact}</a> if you find a problem.</p>
      </Section>

      <Section id="children" title="Children">
        <p>Fennec is a tool for software developers and is not directed at children under 13 (or the minimum age in your country). We don&apos;t knowingly collect their data; if you believe a child has given us data, contact us and we will delete it.</p>
      </Section>

      <Section id="transfers" title="Where data is processed">
        <p>Runta is based in the United States. If you use Fennec from outside the United States, your information may be transferred to, stored and processed in the United States or other countries outside your own.</p>
      </Section>

      <Section id="changes" title="Changes">
        <p>When this policy changes we update the date at the top of this page. If we make material changes to how we use or disclose your information, we will make reasonable efforts to tell you, and take any further steps the law requires.</p>
      </Section>

      <Section id="contact" title="Contact">
        <p>Fennec is operated by Runta Inc., 4 W 4th Ave, Suite 223, San Mateo, CA 94402. Questions about privacy or privacy requests: <a href={'mailto:' + contact}>{contact}</a>.</p>
      </Section>
    </main>
    <SiteFooter/>
  </>;
}

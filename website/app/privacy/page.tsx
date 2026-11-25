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
// ios/Fennec/PrivacyInfo.xcprivacy. Change the date below when the text changes.
const updated = 'November 25, 2026';
const contact = 'hello@runta.com';

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return <section id={id}><h2>{title}</h2>{children}</section>;
}

export default function Privacy() {
  return <>
    <SiteHeader/>
    <main className="legal">
      <h1>Privacy Policy</h1>
      <p className="legal-updated">Last updated {updated}</p>

      <p>Fennec is made by Runta (&ldquo;Runta&rdquo;, &ldquo;we&rdquo;, &ldquo;us&rdquo;). This policy covers Fennec for Mac, Fennec for iPhone, the Fennec cloud computer that Runta runs for your account, and this website. It explains what we collect, why, who else handles it, and how to delete it.</p>
      <p>The short version: we collect what we need to sign you in and run your agents. We don&apos;t sell your data, we don&apos;t show ads, and the apps contain no third-party analytics or tracking.</p>

      <Section id="account" title="Your Runta account">
        <p>Fennec uses a Runta account. When you sign in we receive:</p>
        <ul>
          <li><b>Email address.</b> With email sign-in we send a one-time code to it. Email sign-in is protected by Cloudflare Turnstile, which checks that the request comes from a person.</li>
          <li><b>Profile from Google or GitHub.</b> If you continue with Google or GitHub, we receive your name, email address, profile picture and account ID from that provider. We never see your Google or GitHub password.</li>
          <li><b>Device keys.</b> Each signed-in device gets its own API key, named after the device type and a random four-letter ID, for example &ldquo;Fennec iPhone (ab3k)&rdquo;. It lets you see and revoke each device.</li>
          <li><b>Billing.</b> Your plan, credit balance and cloud computer usage. Payments are handled by our payment processor; we don&apos;t receive or store full card numbers.</li>
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
        <p>We use this content only to provide the service to you: running, showing and resuming your sessions on your devices. We don&apos;t use it for advertising or sell it, and we don&apos;t use it to train AI models.</p>
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
        <p>Like most online services, our servers and Cloudflare, which hosts this website and app downloads, log technical data such as IP addresses, request times, app version and errors. We use it to run, secure and fix the service. This website sets no advertising or analytics cookies.</p>
        <p>If you install a beta through TestFlight and choose to send feedback or crash reports, Apple shares them with us.</p>
      </Section>

      <Section id="sharing" title="Who else handles your data">
        <p>We share data only with service providers that help us run Fennec, and only for that purpose:</p>
        <ul>
          <li>Cloud hosting providers that run your cloud computer and our servers.</li>
          <li>Cloudflare, for this website, downloads and Turnstile.</li>
          <li>Google and GitHub, if you sign in with them.</li>
          <li>Our email provider, to send sign-in codes.</li>
          <li>Our payment processor, for billing.</li>
        </ul>
        <p>We may also disclose data when the law requires it, or to protect the safety of our users and our service. If Runta is involved in a merger or acquisition, this policy continues to apply to your data.</p>
      </Section>

      <Section id="retention" title="Keeping and deleting your data">
        <p>We keep your account and cloud computer data while your account exists. You can stop sessions and delete files on your cloud computer at any time, and sign out to remove your sign-in from a device.</p>
        <p>To delete your Runta account, email <a href={'mailto:' + contact}>{contact}</a> from the address on the account. We delete the account, its cloud computer and its session data within 30 days, except records we must keep by law, such as billing records.</p>
        <p>You can also ask us for a copy of your data or to correct it at the same address. Depending on where you live, you may have further rights under local law, such as the GDPR or CCPA; we honor them.</p>
      </Section>

      <Section id="security" title="Security">
        <p>Connections between your devices, our servers and your cloud computer are encrypted. Sign-in tokens are kept in the Keychain on Apple devices, and agent sign-ins on your cloud computer are stored in private files only your cloud computer&apos;s user can read. No system is perfectly secure, so please tell us at <a href={'mailto:' + contact}>{contact}</a> if you find a problem.</p>
      </Section>

      <Section id="children" title="Children">
        <p>Fennec is a tool for software developers and is not directed at children under 13 (or the minimum age in your country). We don&apos;t knowingly collect their data; if you believe a child has given us data, contact us and we will delete it.</p>
      </Section>

      <Section id="transfers" title="Where data is processed">
        <p>Runta and its providers may process data in countries other than yours, including the United States. Where required, we use appropriate safeguards for these transfers.</p>
      </Section>

      <Section id="changes" title="Changes">
        <p>When this policy changes we update the date at the top of this page. If a change materially affects how we use your data, we will tell you in the app or by email before it takes effect.</p>
      </Section>

      <Section id="contact" title="Contact">
        <p>Questions about privacy: <a href={'mailto:' + contact}>{contact}</a>.</p>
      </Section>
    </main>
    <SiteFooter/>
  </>;
}

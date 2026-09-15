import { ChevronRight, ExternalLink } from 'lucide-react';

const APP_STORE_CONNECT_API_KEYS = 'https://appstoreconnect.apple.com/access/integrations/api';

/** Opens Apple instructions without forwarding the dashboard referrer. */
const AppleLink = ({ href, children }: { href: string; children: React.ReactNode }) => (
  <a
    href={href}
    target="_blank"
    rel="noreferrer"
    className="inline-flex items-center gap-1 font-medium text-foreground underline underline-offset-4">
    {children} <ExternalLink className="h-3 w-3" />
  </a>
);

/** Text exactly as it appears in Apple's interfaces. */
const Ui = ({ children }: { children: React.ReactNode }) => (
  <span className="font-medium text-foreground">{children}</span>
);

/** Renders an ordered list of setup steps. */
const Steps = ({ children }: { children: React.ReactNode }) => (
  <ol className="list-decimal space-y-1.5 pl-5">{children}</ol>
);

/** Wraps optional setup instructions in an expandable section. */
const GuideSection = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <details className="group rounded-lg border">
    <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 text-sm font-medium text-foreground [&::-webkit-details-marker]:hidden">
      <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
      {title}
    </summary>
    <div className="space-y-3 border-t px-4 py-3 text-xs leading-relaxed text-muted-foreground">
      {children}
    </div>
  </details>
);

/** Explains how to export a certificate together with its private key as .p12. */
export const IosCertificateExportGuide = () => (
  <GuideSection title="How to export the .p12">
    <Steps>
      <li>
        Open <Ui>Keychain Access</Ui> on your Mac (press ⌘ Space and type “Keychain Access”).
      </li>
      <li>
        In the left sidebar, select <Ui>login</Ui>, then the <Ui>My Certificates</Ui> tab at the
        top.
      </li>
      <li>
        Find the certificate and click the arrow next to it. A private key must appear underneath.
        No key means the certificate was created on another Mac: export it from that Mac instead.
      </li>
      <li>
        Right-click the certificate line (not the key), then <Ui>Export</Ui>.
      </li>
      <li>
        Set <Ui>File Format</Ui> to <Ui>Personal Information Exchange (.p12)</Ui>, then click{' '}
        <Ui>Save</Ui>.
      </li>
      <li>
        Choose a password and confirm it. macOS may then ask for your Mac login password to allow
        the export.
      </li>
      <li>Upload the .p12 file below and type the password you just chose.</li>
    </Steps>
  </GuideSection>
);

/** Explains where to create and download an App Store Connect team API key. */
export const AppStoreConnectApiKeyGuide = () => (
  <GuideSection title="Create an App Store Connect API key">
    <Steps>
      <li>
        Open <AppleLink href={APP_STORE_CONNECT_API_KEYS}>App Store Connect API</AppleLink>. It is
        under <Ui>Users and Access</Ui> → <Ui>Integrations</Ui> → <Ui>App Store Connect API</Ui>.
      </li>
      <li>
        Select <Ui>Team Keys</Ui> and click <Ui>+</Ui>. Never used the API before? The Account
        Holder clicks <Ui>Request Access</Ui> first.
      </li>
      <li>
        <Ui>Name</Ui>: anything, for example “xprem”.
      </li>
      <li>
        <Ui>Access</Ui>: select <Ui>Admin</Ui>, then click <Ui>Generate</Ui>.
      </li>
      <li>
        Click <Ui>Download</Ui> next to the new key to get the .p8 file. Apple lets you download it
        only once.
      </li>
      <li>
        Copy the <Ui>Key ID</Ui> from the keys table and the <Ui>Issuer ID</Ui> shown above it.
      </li>
    </Steps>
    <p>
      The key can manage the certificates and profiles of your Apple team. It is stored encrypted on
      this server, and only people who can manage credentials use it.
    </p>
  </GuideSection>
);

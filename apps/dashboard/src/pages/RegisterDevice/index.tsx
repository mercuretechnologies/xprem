import { useQuery } from '@tanstack/react-query';
import { useParams, useSearchParams } from 'react-router';
import { QRCodeSVG } from 'qrcode.react';
import { CheckCircle2, Smartphone, TriangleAlert } from 'lucide-react';
import wordmark from '@/assets/xprem-wordmark.svg';
import wordmarkOnLight from '@/assets/xprem-wordmark-on-light.svg';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';

/** iPadOS Safari reports a Mac user agent; the touch points tell them apart. */
const isIos = () =>
  /iPhone|iPad|iPod/.test(navigator.userAgent) ||
  (navigator.userAgent.includes('Macintosh') && navigator.maxTouchPoints > 1);

/** Provides the public registration layout outside the authenticated dashboard. */
const Shell = ({ children }: { children: React.ReactNode }) => (
  <div className="flex min-h-[100dvh] w-full flex-col items-center bg-background px-4 py-8 sm:justify-center">
    <div className="w-full max-w-md">
      <div className="mb-6 flex justify-center">
        <img src={wordmarkOnLight} alt="xprem" className="h-12 w-40 object-cover dark:hidden" />
        <img src={wordmark} alt="xprem" className="hidden h-12 w-40 object-cover dark:block" />
      </div>
      <div className="rounded-lg border bg-card p-6 shadow-elevated sm:p-8">{children}</div>
    </div>
  </div>
);

/** Displays a registration status with its icon and explanatory text. */
const Message = ({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  children?: React.ReactNode;
}) => (
  <div className="flex flex-col items-center gap-3 text-center">
    {icon}
    <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">{title}</h1>
    {children}
  </div>
);

/** Explains that an unknown, expired or revoked invitation must be replaced. */
const InvalidLink = () => (
  <Message
    icon={<TriangleAlert className="h-8 w-8 text-amber-600 dark:text-amber-400" />}
    title="This link is invalid or expired.">
    <p className="text-sm text-muted-foreground">Ask for a new one.</p>
  </Message>
);

/** Explains that a single-use invitation has already registered its device. */
const UsedLink = () => (
  <Message
    icon={<TriangleAlert className="h-8 w-8 text-amber-600 dark:text-amber-400" />}
    title="This link was already used to register an iPhone.">
    <p className="text-sm text-muted-foreground">Ask for a new link.</p>
  </Message>
);

/** Offers a retry when public registration metadata cannot be loaded. */
const LoadError = ({ onRetry }: { onRetry: () => void }) => (
  <Message
    icon={<TriangleAlert className="h-8 w-8 text-amber-600 dark:text-amber-400" />}
    title="This page could not load.">
    <p className="text-sm text-muted-foreground">Check your connection and try again.</p>
    <Button variant="outline" onClick={onRetry}>
      Try again
    </Button>
  </Message>
);

/** Displays a placeholder while the public registration request is pending. */
const Loading = () => (
  <div className="space-y-3">
    <Skeleton className="mx-auto h-6 w-48" />
    <Skeleton className="h-24 w-full" />
  </div>
);

/** Highlights labels the tester must find in the iOS interface. */
const Ui = ({ children }: { children: React.ReactNode }) => (
  <span className="font-semibold text-foreground">{children}</span>
);

/** Fetches and displays the outcome belonging to this invitation and registration ID. */
const RegistrationResult = ({
  token,
  registrationId,
}: {
  token: string;
  registrationId: string;
}) => {
  const statusQuery = useQuery({
    queryKey: ['deviceRegistrationStatus', token, registrationId],
    queryFn: () => api.getDeviceRegistrationStatus(token, registrationId),
    retry: false,
  });

  if (statusQuery.isPending) return <Loading />;
  if (statusQuery.isError) return <LoadError onRetry={() => void statusQuery.refetch()} />;
  if (statusQuery.data.status !== 'ok') return <InvalidLink />;

  const registration = statusQuery.data.data;
  if (registration.status === 'registered') {
    return (
      <Message
        icon={<CheckCircle2 className="h-8 w-8 text-emerald-600 dark:text-emerald-400" />}
        title={`${registration.deviceName || 'This iPhone'} is registered.`}>
        <p className="text-sm text-muted-foreground">
          The next Ad Hoc build will install on it once the profile is updated.
        </p>
      </Message>
    );
  }
  return (
    <Message
      icon={<TriangleAlert className="h-8 w-8 text-destructive" />}
      title="This iPhone could not be registered.">
      {registration.error && <p className="text-sm text-foreground">{registration.error}</p>}
      <p className="text-sm text-muted-foreground">Ask the person who sent the link.</p>
    </Message>
  );
};

/** Shows enrollment instructions on iOS or a QR code for opening the link on a device. */
const RegistrationSteps = ({ token }: { token: string }) => {
  const linkQuery = useQuery({
    queryKey: ['deviceRegistrationLink', token],
    queryFn: () => api.getDeviceRegistrationLink(token),
    retry: false,
  });

  if (linkQuery.isPending) return <Loading />;
  if (linkQuery.isError) return <LoadError onRetry={() => void linkQuery.refetch()} />;
  if (linkQuery.data.status === 'used') return <UsedLink />;
  if (linkQuery.data.status !== 'ok') return <InvalidLink />;

  const link = linkQuery.data.data;
  const subtitle = (
    <p className="text-sm text-muted-foreground">
      <span className="font-medium text-foreground">{link.appName}</span>
      {link.label && ` · ${link.label}`}
    </p>
  );

  if (!isIos()) {
    return (
      <Message
        icon={<Smartphone className="h-8 w-8 text-muted-foreground" />}
        title="Open this page on the iPhone to register">
        {subtitle}
        <div className="rounded-lg border bg-white p-4">
          <QRCodeSVG value={window.location.href} size={192} level="M" marginSize={0} />
        </div>
      </Message>
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1 text-center">
        <h1 className="font-display text-xl font-semibold tracking-tight text-foreground">
          Register this iPhone
        </h1>
        {subtitle}
      </div>

      <ol className="space-y-5">
        <li className="flex gap-3">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-sm font-semibold">
            1
          </span>
          <div className="min-w-0 flex-1 space-y-3 pt-0.5 text-sm text-muted-foreground">
            <p>
              Tap <Ui>Install registration profile</Ui>.
            </p>
            <Button asChild size="lg" className="h-12 w-full text-base">
              <a href={api.deviceRegistrationProfileUrl(token)}>Install registration profile</a>
            </Button>
          </div>
        </li>
        <li className="flex gap-3">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-sm font-semibold">
            2
          </span>
          <p className="pt-0.5 text-sm text-muted-foreground">
            Open <Ui>Settings</Ui> → <Ui>Profile Downloaded</Ui> → <Ui>Install</Ui>. iOS shows “Not
            Verified”: that is expected.
          </p>
        </li>
        <li className="flex gap-3">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-sm font-semibold">
            3
          </span>
          <p className="pt-0.5 text-sm text-muted-foreground">You come back here automatically.</p>
        </li>
      </ol>

      <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs leading-relaxed text-muted-foreground">
        The profile only reads the iPhone identifier and model. You can remove it afterwards in{' '}
        <Ui>Settings</Ui> → <Ui>General</Ui> → <Ui>VPN & Device Management</Ui>.
      </p>
    </div>
  );
};

/** Public page opened on the tester's iPhone: it never uses the dashboard session. */
export const RegisterDevice = () => {
  const { token = '' } = useParams<{ token: string }>();
  const [searchParams] = useSearchParams();
  const registrationId = searchParams.get('registration');

  return (
    <Shell>
      {searchParams.get('error') === 'invalid-link' ? (
        <InvalidLink />
      ) : searchParams.get('error') === 'used' ? (
        <UsedLink />
      ) : registrationId ? (
        <RegistrationResult token={token} registrationId={registrationId} />
      ) : (
        <RegistrationSteps token={token} />
      )}
    </Shell>
  );
};

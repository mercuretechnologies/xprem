// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Filter, Loader2 } from 'lucide-react';
import { api, type ObserveLog } from '@/lib/api';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { deviceName, osLabel } from './deviceNames';
import { sinceLabel } from './format';

const seen = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
});

const Row = ({ label, value }: { label: string; value?: string }) => (
  <div className="flex items-baseline justify-between gap-4 border-b border-border/60 py-2 last:border-0">
    <span className="shrink-0 text-[11px] text-muted-foreground">{label}</span>
    <span className="min-w-0 break-all text-right font-mono text-xs text-foreground">
      {value || <span className="text-muted-foreground/70">Not reported</span>}
    </span>
  </div>
);

const Section = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <section className="mt-5">
    <h3 className="text-[11px] font-medium text-muted-foreground">{title}</h3>
    <div className="mt-1">{children}</div>
  </section>
);

// Everything known about the device that produced one record: what the record
// itself carries (the app and the update as they were at that moment) and what
// the registry knows about the device today (where it is, what it runs now,
// the attributes the app declared about it).
export const DeviceSheet = ({
  log,
  onClose,
  onFilter,
}: {
  log: ObserveLog | null;
  onClose: () => void;
  // Narrow the stream behind the sheet to this device.
  onFilter: (easClientId: string) => void;
}) => {
  // The sheet slides out over a few hundred milliseconds, and `log` is already
  // null by then: without the last one held, it empties before it leaves.
  const [shown, setShown] = useState(log);
  useEffect(() => {
    if (log) setShown(log);
  }, [log]);

  const easClientId = shown?.easClientId ?? '';
  // The sheet opens from a log row, so the account holding it was vetted for
  // observe:read. The registry section below is a different question and a
  // different permission: an account may read telemetry without being allowed
  // to browse devices, and asking anyway would 403 and render as "the registry
  // could not be read", which reports a permission as an outage.
  const canBrowseDevices = useAppPermission('identity:read', 'any-member');
  const deviceQuery = useQuery({
    queryKey: ['identity', 'device', api.getAppId(), easClientId],
    queryFn: () => api.getIdentityDevices({ easClientId: [easClientId] }, undefined, 1),
    enabled: canBrowseDevices && Boolean(log) && Boolean(easClientId),
  });
  const device = deviceQuery.data?.devices[0];

  const model = shown?.deviceModel || device?.deviceModel;
  const attributes = Object.entries(device?.metadata ?? {}).filter(([, value]) => value != null);

  return (
    <Sheet open={Boolean(log)} onOpenChange={open => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        {shown && (
          <>
            <SheetHeader>
              <SheetTitle className="font-mono text-base">
                {easClientId.slice(0, 8)}
                <span className="text-muted-foreground">…</span>
              </SheetTitle>
              <p className="text-xs text-muted-foreground">
                {model ? deviceName(model).label : 'Unknown hardware'}
                {device?.lastSeenAt
                  ? ` · last seen ${sinceLabel(new Date(device.lastSeenAt))}`
                  : ''}
              </p>
            </SheetHeader>

            <div className="mt-4 flex gap-2">
              <Button variant="outline" size="sm" onClick={() => onFilter(easClientId)}>
                <Filter className="h-3.5 w-3.5" />
                Filter on this device
              </Button>
            </div>

            <Section title="Hardware">
              <Row label="Model" value={model ? deviceName(model).label : ''} />
              <Row label="Identifier" value={model} />
              <Row
                label="OS"
                value={osLabel(
                  shown.osName || device?.osName || '',
                  shown.osVersion || device?.osVersion || ''
                )}
              />
              <Row label="Platform" value={shown.platform || device?.platform} />
            </Section>

            {/* What this record was produced by, which is not always what the
                device runs now: it may have moved on since. */}
            <Section title="At this event">
              <Row label="App version" value={shown.appVersion} />
              <Row label="Build number" value={shown.appBuildNumber} />
              <Row label="Update" value={shown.updateId} />
              <Row label="Branch" value={shown.branch} />
              <Row label="Channel" value={shown.channel} />
              <Row label="Runtime" value={shown.runtimeVersion} />
              <Row label="Environment" value={shown.environment} />
              <Row label="Session" value={shown.sessionId} />
              <Row label="SDK" value={shown.sdkVersion} />
            </Section>

            <Section title="Registry">
              {!canBrowseDevices && (
                <p className="py-2 text-xs text-muted-foreground">
                  You do not have permission to browse this app devices, so the registry side of
                  this device is hidden.
                </p>
              )}
              {canBrowseDevices && deviceQuery.isLoading && (
                <p className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Reading the device registry
                </p>
              )}
              {canBrowseDevices && deviceQuery.isError && (
                <p className="py-2 text-xs text-muted-foreground">
                  The device registry could not be read.
                </p>
              )}
              {canBrowseDevices && !deviceQuery.isLoading && !deviceQuery.isError && !device && (
                <p className="py-2 text-xs text-muted-foreground">
                  This device is not in the registry, which happens when it has not polled for an
                  update since telemetry reached the server.
                </p>
              )}
              {device && (
                <>
                  <Row label="Runs now" value={device.currentUpdateId} />
                  <Row label="Branch" value={device.branch} />
                  <Row
                    label="Location"
                    value={[device.city, device.countryCode || shown.countryCode]
                      .filter(Boolean)
                      .join(', ')}
                  />
                  <Row label="First seen" value={seen.format(new Date(device.firstSeenAt))} />
                  <Row label="Last seen" value={seen.format(new Date(device.lastSeenAt))} />
                </>
              )}
            </Section>

            {attributes.length > 0 && (
              <Section title="Attributes">
                {attributes.map(([key, value]) => (
                  <Row key={key} label={key} value={String(value)} />
                ))}
              </Section>
            )}
          </>
        )}
      </SheetContent>
    </Sheet>
  );
};

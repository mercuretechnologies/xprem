import { useState } from 'react';
import { Check, Copy, Database, Smartphone } from 'lucide-react';
import { api } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';
import { Button } from '@/components/ui/button';

const Snippet = ({ label, value }: { label: string; value: string }) => {
  const [copied, setCopied] = useState(false);
  return (
    <div className="mt-3 overflow-hidden rounded-lg border bg-muted/40">
      <div className="flex items-center justify-between border-b bg-muted/40 px-3 py-1.5">
        <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-[11px]"
          onClick={() => {
            void navigator.clipboard.writeText(value);
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1_500);
          }}>
          {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <pre className="overflow-x-auto p-3 font-mono text-[11px] leading-relaxed text-foreground">
        {value}
      </pre>
    </div>
  );
};

// Shown wherever telemetry is the whole point of the page and the server has
// no telemetry store. It is the one place where saying "not configured" is not
// enough: what people need is the two steps that turn it on, with the exact
// endpoint their app has to post to.
export const TelemetryUnavailable = () => {
  const { BASE_URL } = useSettings();
  const appId = api.getAppId();
  const endpointUrl = `${(BASE_URL || '').replace(/\/+$/, '')}/observe/${appId}`;
  const appConfig = `{
  "expo": {
    "extra": {
      "eas": {
        "projectId": "${appId}",
        "observe": { "endpointUrl": "${endpointUrl}" }
      }
    }
  }
}`;

  return (
    <section className="rounded-xl border bg-card p-6 shadow-card">
      <h2 className="font-display text-base font-semibold">Turn on telemetry</h2>
      <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
        Release adoption and launch failures already work without any of this, from the manifest
        polls every client makes. Startup timings, events and logs need the two steps below.
      </p>

      <div className="mt-5 grid gap-5 lg:grid-cols-2">
        <div>
          <div className="flex items-center gap-2">
            <Database className="h-4 w-4 text-primary" />
            <h3 className="text-sm font-medium">1. Give the server somewhere to store it</h3>
          </div>
          <p className="mt-1.5 text-sm text-muted-foreground">
            Set <code className="font-mono text-xs">CLICKHOUSE_URL</code> to a DSN naming a
            dedicated database, then restart. The server runs its own migrations on boot.
          </p>
          <Snippet
            label="Environment"
            value={'CLICKHOUSE_URL=clickhouse://user:password@host:9000/expo_observe'}
          />
        </div>

        <div>
          <div className="flex items-center gap-2">
            <Smartphone className="h-4 w-4 text-primary" />
            <h3 className="text-sm font-medium">2. Point your app at this server</h3>
          </div>
          <p className="mt-1.5 text-sm text-muted-foreground">
            Install <code className="font-mono text-xs">expo-observe</code> (SDK 55+, logs and
            events need 56+) and add the endpoint to your app config. No client code changes beyond
            this.
          </p>
          <Snippet label="app.json" value={appConfig} />
        </div>
      </div>
    </section>
  );
};

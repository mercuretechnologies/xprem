import { ObserveLog } from '@/lib/api';
import { deviceName } from './deviceNames';
import { prettyPayload } from './logRecords';

const Detail = ({ label, value }: { label: string; value: string }) => (
  <div className="min-w-0">
    <dt className="text-[10px] text-muted-foreground">{label}</dt>
    <dd className="mt-1 break-all font-mono text-xs text-foreground">{value || '-'}</dd>
  </div>
);

export const LogDetails = ({ log }: { log: ObserveLog }) => {
  const body = prettyPayload(log.body);
  const attributes = prettyPayload(log.attributes);
  return (
    <div className="border-t bg-muted/30 px-9 py-4">
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Detail label="Device" value={log.easClientId} />
        <Detail label="Session" value={log.sessionId} />
        <Detail label="Update" value={log.updateId} />
        <Detail label="Runtime" value={log.runtimeVersion} />
        <Detail label="Branch" value={log.branch} />
        <Detail label="Channel" value={log.channel} />
        <Detail label="Environment" value={log.environment} />
        <Detail label="App version" value={log.appVersion} />
        <Detail label="Build number" value={log.appBuildNumber} />
        <Detail label="EAS build" value={log.easBuildId} />
        <Detail
          label="Device model"
          value={log.deviceModel ? deviceName(log.deviceModel).label : ''}
        />
        <Detail label="Country" value={log.countryCode} />
        <Detail label="OS" value={`${log.osName} ${log.osVersion}`.trim()} />
      </dl>
      {body && (
        <div className="mt-4">
          <div className="mb-1.5 text-[10px] text-muted-foreground">Message</div>
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-card p-3 font-mono text-[11px] leading-relaxed text-foreground">
            {body}
          </pre>
        </div>
      )}
      {attributes && attributes !== '{}' && (
        <div className="mt-4">
          <div className="mb-1.5 text-[10px] text-muted-foreground">Attributes</div>
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-card p-3 font-mono text-[11px] leading-relaxed text-muted-foreground">
            {attributes}
          </pre>
        </div>
      )}
    </div>
  );
};

import { useState } from 'react';
import { Bot, Check, Copy } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/Combobox';
import { PageHeader } from '@/components/PageHeader';
import { useSettings } from '@/lib/SettingsContext';
import { ClaudeIcon, CursorIcon, GeminiIcon, OpenAiIcon, VsCodeIcon } from './agentIcons';

type AgentId = 'claude-code' | 'claude' | 'cursor' | 'vscode' | 'codex' | 'gemini-cli' | 'other';

const agents: { value: AgentId; label: string; icon: React.ReactNode }[] = [
  { value: 'claude-code', label: 'Claude Code', icon: <ClaudeIcon /> },
  { value: 'claude', label: 'Claude (desktop and web)', icon: <ClaudeIcon /> },
  { value: 'cursor', label: 'Cursor', icon: <CursorIcon /> },
  { value: 'vscode', label: 'VS Code', icon: <VsCodeIcon /> },
  { value: 'codex', label: 'Codex', icon: <OpenAiIcon /> },
  { value: 'gemini-cli', label: 'Gemini CLI', icon: <GeminiIcon /> },
  { value: 'other', label: 'Other', icon: <Bot /> },
];

const Code = ({ children }: { children: React.ReactNode }) => (
  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">{children}</code>
);

const Snippet = ({ label, value }: { label: string; value: string }) => {
  const [copied, setCopied] = useState(false);
  return (
    <div className="overflow-hidden rounded-lg border bg-muted/40">
      <div className="flex items-center justify-between border-b bg-muted/40 px-3 py-1.5">
        <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-[11px]"
          onClick={() => {
            // navigator.clipboard is undefined over plain HTTP, and a rejected
            // write must not flip the label to "Copied".
            void navigator.clipboard
              ?.writeText(value)
              .then(() => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1_500);
              })
              .catch(() => setCopied(false));
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

const mcpServersBlock = (url: string) => `{
  "mcpServers": {
    "xprem": {
      "url": "${url}"
    }
  }
}`;

const Instructions = ({ agent, url }: { agent: AgentId; url: string }) => {
  switch (agent) {
    case 'claude-code':
      return (
        <>
          <p>
            Run the command, then type <Code>/mcp</Code> in Claude Code to sign in.
          </p>
          <Snippet label="Terminal" value={`claude mcp add --transport http xprem ${url}`} />
        </>
      );
    case 'claude':
      return (
        <>
          <p>
            Open <strong>Customize › Connectors</strong>, click <strong>+</strong> then{' '}
            <strong>Add custom connector</strong>, paste the URL and click <strong>Add</strong>. On
            a Team or Enterprise plan, an owner adds it from{' '}
            <strong>Organization settings › Connectors</strong> and each member clicks{' '}
            <strong>Connect</strong>.
          </p>
          <Snippet label="Remote MCP server URL" value={url} />
        </>
      );
    case 'cursor':
      return (
        <>
          <p>
            Add the server to <Code>~/.cursor/mcp.json</Code>, or to <Code>.cursor/mcp.json</Code>{' '}
            for a single project. Cursor opens the sign-in page on first use.
          </p>
          <Snippet label="mcp.json" value={mcpServersBlock(url)} />
        </>
      );
    case 'vscode':
      return (
        <>
          <p>
            Add the server to <Code>.vscode/mcp.json</Code>, or run <strong>MCP: Add Server</strong>{' '}
            from the command palette and pick HTTP.
          </p>
          <Snippet
            label=".vscode/mcp.json"
            value={`{
  "servers": {
    "xprem": {
      "type": "http",
      "url": "${url}"
    }
  }
}`}
          />
        </>
      );
    case 'codex':
      return (
        <>
          <p>
            Add the server to <Code>~/.codex/config.toml</Code>, then sign in. The CLI, the IDE
            extension and the desktop app share this file.
          </p>
          <Snippet
            label="~/.codex/config.toml"
            value={`[mcp_servers.xprem]
url = "${url}"`}
          />
          <Snippet label="Terminal" value="codex mcp login xprem" />
        </>
      );
    case 'gemini-cli':
      return (
        <>
          <p>Run the command. Gemini CLI detects the sign-in step on its own.</p>
          <Snippet label="Terminal" value={`gemini mcp add --transport http xprem ${url}`} />
        </>
      );
    case 'other':
      return (
        <>
          <p>
            Any client that supports remote MCP servers over streamable HTTP with OAuth can connect.
            Give it the URL; most clients accept this block in their MCP configuration file.
          </p>
          <Snippet label="MCP server URL" value={url} />
          <Snippet label="MCP configuration" value={mcpServersBlock(url)} />
        </>
      );
  }
};

export const Mcp = () => {
  const { BASE_URL } = useSettings();
  const endpointUrl = `${(BASE_URL || '').replace(/\/+$/, '')}/mcp`;
  const [agent, setAgent] = useState<AgentId>('claude-code');
  const selected = agents.find(a => a.value === agent) ?? agents[0];

  return (
    <div className="w-full">
      <PageHeader
        title="MCP"
        description={
          <>
            Connect an AI agent to this server at <Code>{endpointUrl}</Code>. The first time a
            client connects, your browser opens this dashboard's sign-in and consent page. The agent
            then acts with your account and your permissions.
          </>
        }
      />

      <section className="max-w-2xl overflow-hidden rounded-xl border bg-card shadow-card">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/40 px-5 py-3">
          <h2 className="text-sm font-medium">Your agent</h2>
          <Combobox
            className="w-64"
            label="Select an agent"
            options={agents}
            value={agent}
            onChange={value => {
              // Re-selecting the current option toggles it to '': keep it.
              if (value) setAgent(value as AgentId);
            }}
          />
        </div>
        <div className="space-y-3 px-5 py-4 text-sm leading-relaxed text-muted-foreground">
          <div className="flex items-center gap-2 text-foreground [&_svg]:size-4">
            {selected.icon}
            <h3 className="text-sm font-medium">{selected.label}</h3>
          </div>
          <Instructions agent={agent} url={endpointUrl} />
        </div>
      </section>
    </div>
  );
};

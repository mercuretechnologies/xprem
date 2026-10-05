import spawnAsync from '@expo/spawn-async';
import { ChildProcess } from 'child_process';

import { formatBuildError } from './errors';
import { LogWriter } from './log';
import { streamBuildOutput } from './output';

export interface BuildCommand {
  title: string;
  command: string;
  args: string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  // Rewrites or drops (undefined) an output line before it reaches the build log.
  transform?: (line: string) => string | undefined;
  // Warns, then stops the command, once it has printed nothing for this long.
  silence?: { warnAfterMs: number; stopAfterMs: number };
}

interface RunningCommand {
  child: ChildProcess;
  stopping?: Promise<void>;
}

let active: RunningCommand | undefined;

// Cancellation includes detached descendants, such as Gradle's single-use daemon.
export async function terminateBuildCommand(): Promise<void> {
  if (active) {
    await (active.stopping ??= stopProcessTree(active.child.pid));
  }
}

export async function runBuildCommand(
  { title, command, args, cwd, env, transform, silence }: BuildCommand,
  log: LogWriter,
  secrets: string[]
): Promise<void> {
  let stopped = false;
  let timers: NodeJS.Timeout[] = [];
  try {
    const running = spawnAsync(command, args, { cwd, env });
    const current: RunningCommand = { child: running.child };
    active = current;
    if (silence) {
      const minutes = (ms: number): string => `${Math.round(ms / 60000)} minutes`;
      timers = [
        setTimeout(() => {
          log.warn(`${title} has printed nothing for ${minutes(silence.warnAfterMs)}.`);
        }, silence.warnAfterMs),
        setTimeout(() => {
          stopped = true;
          // The command's finally block awaits and reports cancellation failures.
          void terminateBuildCommand().catch(() => {});
        }, silence.stopAfterMs),
      ];
    }
    const streams = (['stdout', 'stderr'] as const).flatMap(source => {
      const stream = running.child?.[source];
      return stream
        ? [
            streamBuildOutput(stream, secrets, line => {
              timers.forEach(timer => timer.refresh());
              const shown = transform ? transform(line) : line;
              if (shown !== undefined) {
                log.write(shown, source);
              }
            }),
          ]
        : [];
    });
    try {
      await running;
    } finally {
      timers.forEach(timer => {
        clearTimeout(timer);
      });
      streams.forEach(stream => {
        stream.close();
      });
      try {
        await current.stopping;
      } finally {
        active = undefined;
      }
    }
  } catch (error) {
    if (stopped && silence) {
      throw new Error(
        `${title} was stopped: it printed nothing for ${Math.round(
          silence.stopAfterMs / 60000
        )} minutes.`
      );
    }
    throw new Error(formatBuildError(title, error, secrets));
  }
}

interface OwnedProcess {
  pid: number;
  parent: number;
  state: string;
  started: string;
}

// Snapshot descendants before cancellation reparents them, and check their start times
// before signalling survivors so a recycled PID cannot target another command.
async function stopProcessTree(pid?: number): Promise<void> {
  if (!pid) {
    return;
  }
  const processes = await readProcesses();
  const owned: OwnedProcess[] = [];
  const collect = (parent: number): void => {
    const current = processes.find(candidate => candidate.pid === parent);
    if (current) {
      owned.push(current);
    }
    processes
      .filter(candidate => candidate.parent === parent)
      .forEach(child => {
        collect(child.pid);
      });
  };
  collect(pid);
  let deadline = Date.now() + 5000;
  let signalled = false;
  let forced = false;
  while (owned.length) {
    const current = await readProcesses(owned.map(process => process.pid));
    const remaining = owned.filter(process =>
      current.some(
        candidate =>
          candidate.pid === process.pid &&
          candidate.started === process.started &&
          !candidate.state.startsWith('Z')
      )
    );
    if (!remaining.length) {
      return;
    }
    if (!signalled) {
      remaining.reverse().forEach(process => {
        signalProcess(process.pid, 'SIGTERM');
      });
      signalled = true;
    }
    if (Date.now() >= deadline) {
      if (forced) {
        throw new Error('Could not stop the build process tree.');
      }
      remaining.reverse().forEach(process => {
        signalProcess(process.pid, 'SIGKILL');
      });
      forced = true;
      deadline = Date.now() + 5000;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
}

function signalProcess(pid: number, signal: NodeJS.Signals): void {
  try {
    process.kill(pid, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ESRCH') {
      throw error;
    }
  }
}

async function readProcesses(pids?: number[]): Promise<OwnedProcess[]> {
  const { stdout } = await spawnAsync(
    'ps',
    [...(pids ? ['-p', pids.join(',')] : ['-ax']), '-o', 'pid=,ppid=,stat=,lstart='],
    { env: { ...process.env, LC_ALL: 'C' } }
  ).catch(error => {
    if (pids && error.status === 1) {
      return { stdout: '' };
    }
    throw error;
  });
  return stdout.split('\n').flatMap(line => {
    const match = /^\s*(\d+)\s+(\d+)\s+(\S+)\s+(.+?)\s*$/.exec(line);
    return match
      ? [{ pid: Number(match[1]), parent: Number(match[2]), state: match[3], started: match[4] }]
      : [];
  });
}

// Spinner drawn with the @clack/prompts spinner so it matches the CLI's
// visual identity. Keeps the ora-like call surface the commands already use
// (start/succeed/fail/warn/stop). In CI, non-TTY or debug runs the spinner
// degrades to plain log lines so output stays deterministic.
import * as clack from '@clack/prompts';
import chalk from 'chalk';
import { boolish } from 'getenv';

import Log from './log';

export type Spinner = {
  start(text?: string): Spinner;
  /** Replaces the spinner line while it keeps spinning; silent when degraded. */
  update(text: string): Spinner;
  succeed(text?: string): Spinner;
  fail(text?: string): Spinner;
  warn(text?: string): Spinner;
  stop(): Spinner;
};

const isCi = boolish('CI', false);

export function ora(options?: string | { text?: string }): Spinner {
  const disabled = Log.isDebug || !process.stdin.isTTY || isCi;
  let text = typeof options === 'string' ? options : options?.text ?? '';
  let active: ReturnType<typeof clack.spinner> | undefined;

  const finish = (message: string, code: number, fallback: (message: string) => void): void => {
    if (active) {
      active.stop(message, code);
      active = undefined;
    } else {
      fallback(message);
    }
  };

  const spinner: Spinner = {
    start(nextText?: string) {
      text = nextText ?? text;
      if (disabled) {
        Log.log(text);
      } else {
        active = clack.spinner();
        active.start(text);
      }
      return spinner;
    },
    update(nextText: string) {
      text = nextText;
      active?.message(nextText);
      return spinner;
    },
    succeed(nextText?: string) {
      finish(nextText ?? text, 0, message => {
        Log.succeed(message);
      });
      return spinner;
    },
    fail(nextText?: string) {
      finish(nextText ?? text, 2, message => {
        Log.fail(message);
      });
      return spinner;
    },
    warn(nextText?: string) {
      finish(chalk.yellow(nextText ?? text), 0, message => {
        Log.warn(message);
      });
      return spinner;
    },
    stop() {
      finish(chalk.dim(text), 0, () => {});
      return spinner;
    },
  };
  return spinner;
}

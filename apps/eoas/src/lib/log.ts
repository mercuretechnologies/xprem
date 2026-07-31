// Rendering for every command's output, drawn with the @clack/prompts
// primitives so the whole CLI shares one visual identity (gutter, symbols,
// notes). The static API is kept from the original eas-cli logger so call
// sites did not have to change.
import * as clack from '@clack/prompts';
import chalk from 'chalk';
import { boolish } from 'getenv';
import terminalLink from 'terminal-link';
import { format } from 'util';

type Color = (...text: string[]) => string;

export default class Log {
  public static readonly isDebug = boolish('DEBUG', false);

  public static log(...args: any[]): void {
    Log.write(format(...args));
  }

  public static newLine(): void {
    Log.write('');
    Log.isLastLineNewLine = true;
  }

  public static addNewLineIfNone(): void {
    if (!Log.isLastLineNewLine) {
      Log.newLine();
    }
  }

  public static error(...args: any[]): void {
    Log.track();
    clack.log.error(Log.withTextColor(args, chalk.red).join(' '));
  }

  public static warn(...args: any[]): void {
    Log.track();
    clack.log.warn(Log.withTextColor(args, chalk.yellow).join(' '));
  }

  public static debug(...args: any[]): void {
    if (Log.isDebug) {
      Log.write(format(...args));
    }
  }

  public static gray(...args: any[]): void {
    Log.write(Log.withTextColor(args, chalk.gray).join(' '));
  }

  public static warnDeprecatedFlag(flag: string, message: string): void {
    Log.warn(`› ${chalk.bold('--' + flag)} flag is deprecated. ${message}`);
  }

  public static fail(message: string): void {
    Log.track();
    clack.log.error(message);
  }

  public static succeed(message: string): void {
    Log.track();
    clack.log.success(message);
  }

  public static withTick(...args: any[]): void {
    Log.track();
    clack.log.success(format(...args));
  }

  public static withInfo(...args: any[]): void {
    Log.track();
    clack.log.info(format(...args));
  }

  /** Opens a clack session frame; every line after it hangs on the gutter. */
  public static intro(title: string): void {
    Log.track();
    clack.intro(title);
  }

  /** Closes the clack session frame opened by intro. */
  public static outro(message: string): void {
    Log.track();
    clack.outro(message);
  }

  public static note(content: string, title?: string): void {
    Log.track();
    clack.note(content, title);
  }

  public static cancel(message: string): void {
    Log.track();
    clack.cancel(message);
  }

  private static write(text: string): void {
    Log.track(text);
    clack.log.message(text);
  }

  private static withTextColor(args: any[], chalkColor: Color): string[] {
    return args.map(arg => chalkColor(format(arg)));
  }

  private static isLastLineNewLine = false;
  private static track(text?: string): void {
    Log.isLastLineNewLine = text !== undefined && text === '';
  }
}

/**
 * Prints a link for given URL, using text if provided, otherwise text is just the URL.
 * Format links as dim (unless disabled) and with an underline.
 *
 * @example https://expo.dev
 */
export function link(
  url: string,
  { text = url, fallback, dim = true }: { text?: string; dim?: boolean; fallback?: string } = {}
): string {
  // Links can be disabled via env variables https://github.com/jamestalmage/supports-hyperlinks/blob/master/index.js
  const output = terminalLink(text, url, {
    fallback: () =>
      fallback ?? (text === url ? chalk.underline(url) : `${text}: ${chalk.underline(url)}`),
  });
  return dim ? chalk.dim(output) : output;
}

/**
 * Provide a consistent "Learn more" link experience.
 * Format links as dim (unless disabled) with an underline.
 *
 * @example Learn more: https://expo.dev
 */
export function learnMore(
  url: string,
  {
    learnMoreMessage: maybeLearnMoreMessage,
    dim = true,
  }: { learnMoreMessage?: string; dim?: boolean } = {}
): string {
  return link(url, { text: maybeLearnMoreMessage ?? 'Learn more', dim });
}

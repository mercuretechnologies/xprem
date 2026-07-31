// This file is copied from eas-cli[https://github.com/expo/eas-cli] to ensure consistent user experience across the CLI.
import * as clack from '@clack/prompts';
import chalk from 'chalk';
import { constants } from 'os';
import prompts, { Answers, Choice, Options } from 'prompts';

export interface ExpoChoice<T> extends Choice {
  value: T;
}

export async function promptAsync<T extends string = string>(
  questions: prompts.PromptObject<T> | prompts.PromptObject<T>[],
  options: Options = {}
): Promise<Answers<T>> {
  if (!process.stdin.isTTY) {
    const message = Array.isArray(questions) ? questions[0]?.message : questions.message;
    throw new Error(
      `Input is required, but stdin is not readable. Failed to display prompt: ${message}`
    );
  }
  return await prompts<T>(questions, {
    onCancel() {
      process.exit(constants.signals.SIGINT + 128); // Exit code 130 used when process is interrupted with ctrl+c.
    },
    ...options,
  });
}

export async function confirmAsync(
  question: prompts.PromptObject<any>,
  options?: Options
): Promise<boolean> {
  const { value } = await promptAsync(
    {
      initial: true,
      ...question,
      name: 'value',
      type: 'confirm',
    },
    options
  );
  return value;
}

export async function selectAsync<T>(
  message: string,
  choices: ExpoChoice<T>[],
  config?: {
    options?: Options;
    initial?: T;
    warningMessageForDisabledEntries?: string;
  }
): Promise<T> {
  const initial = config?.initial ? choices.findIndex(({ value }) => value === config.initial) : 0;
  const { value } = await promptAsync(
    {
      message,
      choices,
      initial,
      name: 'value',
      type: 'select',
      warn: config?.warningMessageForDisabledEntries,
    },
    config?.options ?? {}
  );
  return value ?? null;
}

export async function toggleConfirmAsync(
  questions: prompts.PromptObject<any>,
  options?: Options
): Promise<boolean> {
  const { value } = await promptAsync(
    {
      active: 'yes',
      inactive: 'no',
      ...questions,
      name: 'value',
      type: 'toggle',
    },
    options
  );
  return value ?? null;
}

/** Returned by the clack-based prompts below when the user asks to go back one step. */
export const BACK = Symbol('back');
export const BACK_INPUT = '<';

function ensureNotCancelled<T>(value: T | symbol): T {
  if (clack.isCancel(value)) {
    clack.cancel('Cancelled, nothing was written.');
    process.exit(constants.signals.SIGINT + 128);
  }
  return value;
}

export type TextStepOptions = {
  initial?: string;
  /** Empty answers are allowed and become undefined. */
  optional?: boolean;
  secret?: boolean;
  allowBack?: boolean;
  validate?: (value: string) => true | string;
};

/**
 * clack redraws break as soon as a prompt line wraps, so the message plus the
 * suffix must stay well under a terminal width; keep messages short and never
 * use clack placeholders (Tab types them into the buffer and a skipped answer
 * renders them as if submitted).
 */
export async function textStep(
  message: string,
  options: TextStepOptions = {}
): Promise<string | undefined | typeof BACK> {
  const hints: string[] = [];
  if (options.optional) {
    hints.push('optional');
  }
  if (options.allowBack) {
    hints.push(`${BACK_INPUT} back`);
  }
  const suffix = hints.length > 0 ? ` ${chalk.dim(`(${hints.join(' · ')})`)}` : '';
  const validate = (v: string | undefined): string | undefined => {
    const value = v ?? '';
    if ((options.allowBack && value === BACK_INPUT) || (options.optional && value === '')) {
      return undefined;
    }
    if (!options.validate) {
      return options.optional || value !== '' ? undefined : 'Cannot be empty';
    }
    const result = options.validate(value);
    return result === true ? undefined : result;
  };
  const value = ensureNotCancelled(
    options.secret
      ? await clack.password({ message: `${message}${suffix}`, validate })
      : await clack.text({
          message: `${message}${suffix}`,
          initialValue: options.initial,
          validate,
        })
  );
  const answer = (value ?? '').trim();
  if (options.allowBack && answer === BACK_INPUT) {
    return BACK;
  }
  return answer !== '' ? answer : undefined;
}

export type SelectStepChoice<T> = { title: string; value: T; description?: string };

// Selection happens on indices (clack's Option type does not resolve with an
// open generic); -1 is the Back entry.
export async function selectStep<T>(
  message: string,
  choices: SelectStepChoice<T>[],
  options: { allowBack: boolean; initial?: T }
): Promise<T | typeof BACK> {
  const list = choices.map((choice, index) => ({
    value: index,
    label: choice.title,
    hint: choice.description,
  }));
  if (options.allowBack) {
    list.push({ value: -1, label: '← Back', hint: 'previous step' });
  }
  const initialIndex =
    options.initial === undefined
      ? undefined
      : choices.findIndex(choice => choice.value === options.initial);
  const picked = ensureNotCancelled(
    await clack.select({
      message,
      options: list,
      initialValue: initialIndex !== undefined && initialIndex >= 0 ? initialIndex : undefined,
    })
  );
  return picked === -1 ? BACK : choices[picked].value;
}

export async function yesNoStep(
  message: string,
  options: { allowBack: boolean; initial?: boolean }
): Promise<boolean | typeof BACK> {
  return await selectStep<boolean>(
    message,
    [
      { title: 'Yes', value: true },
      { title: 'No', value: false },
    ],
    { allowBack: options.allowBack, initial: options.initial ?? false }
  );
}

export async function confirmStep(message: string, initial = false): Promise<boolean> {
  return ensureNotCancelled(await clack.confirm({ message, initialValue: initial }));
}

export async function pressAnyKeyToContinueAsync(): Promise<void> {
  process.stdin.setRawMode(true);
  process.stdin.resume();
  process.stdin.setEncoding('utf8');

  await new Promise<void>(res => {
    process.stdin.on('data', key => {
      if (String(key) === '\u0003') {
        process.exit(constants.signals.SIGINT + 128); // ctrl-c
      }
      res();
    });
  });
}

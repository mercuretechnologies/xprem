// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { Check, ChevronsUpDown, Plus, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

const plural = (label: string) => {
  const lower = label.toLowerCase();
  return /(?:s|x|z|ch|sh)$/.test(lower) ? `${lower}es` : `${lower}s`;
};

// What the trigger says. One value reads as itself, several read as a count:
// "main" tells you what you picked, "3 branches" tells you that you are
// comparing without pretending to fit three names in a button.
const triggerLabel = (label: string, values: string[], display: (value: string) => string) => {
  if (values.length === 0) return label;
  if (values.length === 1) return display(values[0]);
  return `${values.length} ${plural(label)}`;
};

// Keywords match the search box without being displayed: a full UUID is worth
// finding by and worth nobody reading.
type Option = {
  value: string;
  label: string;
  detail?: string;
  group?: string;
  keywords?: string[];
};

// Options that name a section are collected under it, in the order the
// sections first appear, so what several rows share is written once as a
// heading instead of on every line. Options without one stay a flat list.
const sections = (options: Option[]) => {
  const byHeading = new Map<string, Option[]>();
  for (const option of options) {
    const heading = option.group ?? '';
    const items = byHeading.get(heading);
    if (items) items.push(option);
    else byHeading.set(heading, [option]);
  }
  return Array.from(byHeading, ([heading, items]) => ({ heading, items }));
};

export const MultiSelect = ({
  label,
  values,
  options,
  onToggle,
  onClear,
  loading,
  disabled,
  className,
  display = value => value,
  groupIcon,
}: {
  label: string;
  values: string[];
  options: Option[];
  onToggle: (value: string) => void;
  onClear: () => void;
  loading?: boolean;
  disabled?: boolean;
  className?: string;
  display?: (value: string) => string;
  // Drawn before every section heading, to say what the sections are without
  // spelling it out on each one.
  groupIcon?: React.ReactNode;
}) => {
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          disabled={disabled}
          aria-expanded={open}
          className={cn(
            'w-max justify-between font-normal',
            values.length > 0 && 'border-primary/25 bg-primary/[0.07]',
            className
          )}>
          <span className="min-w-0 flex-1 truncate text-left">
            {triggerLabel(label, values, display)}
          </span>
          {values.length > 0 ? (
            <span
              aria-hidden="true"
              className="ml-2 rounded-sm p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
              onClick={event => {
                event.preventDefault();
                event.stopPropagation();
                onClear();
                setOpen(false);
              }}>
              <X className="h-3.5 w-3.5" />
            </span>
          ) : (
            <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
          )}
        </Button>
      </PopoverTrigger>
      {/* Wide enough that two publishes sharing a message prefix stay
          distinguishable, and never wider than the screen. */}
      <PopoverContent align="start" className="w-[min(30rem,calc(100vw-2rem))] p-0">
        <Command>
          <CommandInput placeholder={`Search ${label.toLowerCase()}…`} />
          <CommandList>
            <CommandEmpty>{loading ? 'Loading…' : 'Nothing found.'}</CommandEmpty>
            {sections(options).map(section => (
              <CommandGroup
                key={section.heading}
                heading={
                  section.heading ? (
                    <span className="flex items-center gap-1.5">
                      {groupIcon}
                      <span className="truncate">{section.heading}</span>
                    </span>
                  ) : undefined
                }>
                {section.items.map(option => {
                  const selected = values.includes(option.value);
                  return (
                    <CommandItem
                      key={option.value}
                      // Searchable by everything on the row plus the heading it
                      // sits under, so typing a short id or a branch finds it
                      // as readily as typing the message.
                      value={`${option.label} ${option.detail ?? ''} ${option.group ?? ''}`}
                      keywords={option.keywords}
                      // The popover stays open: picking several values is the
                      // point, and reopening it between each is a chore.
                      onSelect={() => onToggle(option.value)}
                      className="items-start">
                      <span
                        className={cn(
                          'mr-2 mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border',
                          selected
                            ? 'border-primary bg-primary text-primary-foreground'
                            : 'border-input'
                        )}>
                        {selected && <Check className="h-3 w-3" />}
                      </span>
                      {/* Wrapping rather than truncating: a message is what
                          tells two publishes apart, and it is often long. */}
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <span className="whitespace-normal break-words">{option.label}</span>
                        {option.detail && (
                          <span className="whitespace-normal break-words text-[11px] text-muted-foreground">
                            {option.detail}
                          </span>
                        )}
                      </span>
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
};

// Free text that can hold several values: type, press Enter, get a chip. The
// values are exact matches server-side, so there is nothing to search through.
export const MultiTextInput = ({
  values,
  onChange,
  placeholder,
  transform = value => value,
}: {
  values: string[];
  onChange: (values: string[]) => void;
  placeholder: string;
  transform?: (value: string) => string;
}) => {
  const [draft, setDraft] = useState('');

  const commit = () => {
    const value = transform(draft.trim());
    if (!value || values.includes(value)) {
      setDraft('');
      return;
    }
    onChange([...values, value]);
    setDraft('');
  };

  return (
    <div className="space-y-1.5">
      <div className="flex gap-1.5">
        <Input
          value={draft}
          onChange={event => setDraft(event.target.value)}
          onKeyDown={event => {
            if (event.key === 'Enter') {
              event.preventDefault();
              commit();
            }
            // Backspace on an empty field removes the last chip, the usual
            // behaviour of a token field.
            if (event.key === 'Backspace' && draft === '' && values.length > 0) {
              onChange(values.slice(0, -1));
            }
          }}
          placeholder={placeholder}
          className="font-mono text-xs"
        />
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Add value"
          disabled={draft.trim() === ''}
          onClick={commit}>
          <Plus className="h-3.5 w-3.5" />
        </Button>
      </div>
      {values.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {values.map(value => (
            <span
              key={value}
              className="flex items-center gap-1 rounded-full border border-primary/20 bg-primary/[0.07] py-0.5 pl-2 pr-1 font-mono text-[11px]">
              {value}
              <button
                type="button"
                aria-label={`Remove ${value}`}
                onClick={() => onChange(values.filter(entry => entry !== value))}
                className="rounded-full p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground">
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  );
};

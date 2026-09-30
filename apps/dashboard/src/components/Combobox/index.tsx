'use client';

import * as React from 'react';
import { Check, ChevronsUpDown, X } from 'lucide-react';

import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

// Sentinel for the pinned action row. It is not a selectable option, so it gets
// its own value and is excluded from search filtering.
const ACTION_VALUE = '__combobox_action__';

interface ComboboxProps {
  options: { value: string; label: string; icon?: React.ReactNode }[];
  value: string;
  onChange: (value: string) => void;
  loading?: boolean;
  label?: string;
  // Disables the trigger entirely (e.g. while the surrounding form saves).
  disabled?: boolean;
  // Optional action pinned under the options (e.g. "New Application"). Stays
  // visible whatever the search input, since it is not one of the options.
  action?: { label: string; icon?: React.ReactNode; onSelect: () => void };
  clearable?: boolean;
  // Extra classes for the trigger button; pass "w-full" to make the combobox
  // fill its container (the popover always matches the trigger width).
  className?: string;
}

export function Combobox(props: ComboboxProps) {
  const { options, value, onChange, loading, label, disabled, action, clearable, className } =
    props;
  const [open, setOpen] = React.useState(false);
  const selected = options.find(opt => opt.value === value);
  // Disabling only blocks the trigger: a popover already open when disabled
  // flips to true (e.g. the surrounding form starts saving) would stay
  // interactive, so close it.
  React.useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={disabled ? false : open}
          disabled={disabled}
          className={cn('w-max justify-between font-normal', className)}>
          {selected?.icon}
          <span className="min-w-0 flex-1 truncate text-left">
            {value ? selected?.label || value : label || 'Select option'}
          </span>
          {clearable && value && (
            // Pointer-only shortcut: a focusable control nested in the trigger
            // <button> would be invalid HTML with browser-dependent keyboard
            // behavior. Keyboard users clear by re-selecting the selected
            // option (onSelect toggles it to '').
            <span
              aria-hidden="true"
              className="ml-2 rounded-sm p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
              onClick={event => {
                event.preventDefault();
                event.stopPropagation();
                onChange('');
                setOpen(false);
              }}>
              <X className="h-3.5 w-3.5" />
            </span>
          )}
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[max(var(--radix-popover-trigger-width),12rem)] p-0">
        <Command
          filter={(itemValue, search) => {
            if (itemValue === ACTION_VALUE) return 1;
            const matchedOption = options.find(opt => opt.value === itemValue);
            const textToSearch = matchedOption ? matchedOption.label : itemValue;
            return textToSearch.toLowerCase().includes(search.toLowerCase()) ? 1 : 0;
          }}>
          <CommandInput placeholder="Search..." />
          <CommandList>
            <CommandEmpty>No option found.</CommandEmpty>
            <CommandGroup>
              {options.map(opt => (
                <CommandItem
                  key={opt.value}
                  value={opt.value}
                  onSelect={() => {
                    onChange(opt.value === value ? '' : opt.value);
                    setOpen(false);
                  }}>
                  <Check
                    className={cn(
                      'mr-2 h-4 w-4',
                      value === opt.value ? 'opacity-100' : 'opacity-0'
                    )}
                  />
                  {opt.icon}
                  {opt.label}
                </CommandItem>
              ))}
              {loading && <CommandItem disabled>Loading...</CommandItem>}
            </CommandGroup>
            {action && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem
                    value={ACTION_VALUE}
                    onSelect={() => {
                      action.onSelect();
                      setOpen(false);
                    }}
                    className="text-muted-foreground">
                    {action.icon}
                    {action.label}
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

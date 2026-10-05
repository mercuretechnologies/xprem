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
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

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
  // A portaled list inside a dialog needs its own scroll lock so touch and
  // wheel events are handled by the list rather than blocked by the dialog.
  modal?: boolean;
  // Optional action pinned under the options (e.g. "New Application"). Stays
  // visible whatever the search input, since it is not one of the options.
  action?: { label: string; icon?: React.ReactNode; onSelect: () => void };
  clearable?: boolean;
  // Extra classes for the trigger button; pass "w-full" to make the combobox
  // fill its container (the popover always matches the trigger width).
  className?: string;
}

export function Combobox(props: ComboboxProps) {
  const {
    options,
    value,
    onChange,
    loading,
    label,
    disabled,
    modal,
    action,
    clearable,
    className,
  } = props;
  const [open, setOpen] = React.useState(false);
  const openedByTouch = React.useRef(false);
  const triggerRef = React.useRef<HTMLButtonElement>(null);
  const contentRef = React.useRef<HTMLDivElement>(null);
  const viewportAnchor = React.useRef({
    get contextElement() {
      return triggerRef.current ?? undefined;
    },
    getBoundingClientRect() {
      const rect = triggerRef.current?.getBoundingClientRect() ?? new DOMRect();
      const viewport = window.visualViewport;
      // Floating UI adds the visual viewport offset itself on WebKit.
      const origin = CSS.supports('-webkit-backdrop-filter', 'none')
        ? 0
        : (viewport?.offsetTop ?? 0);
      const top = origin + 8;
      const bottom = origin + (viewport?.height ?? window.innerHeight) - 8;
      const height = Math.min(rect.height, Math.max(0, bottom - top));
      // The keyboard can hide the trigger while search is focused. Keep its
      // positioning anchor visible so the options fit above the keyboard.
      return new DOMRect(
        rect.x,
        Math.min(Math.max(rect.y, top), bottom - height),
        rect.width,
        height
      );
    },
  });
  const selected = options.find(opt => opt.value === value);
  // Disabling only blocks the trigger: a popover already open when disabled
  // flips to true (e.g. the surrounding form starts saving) would stay
  // interactive, so close it.
  React.useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  return (
    <Popover open={open} onOpenChange={setOpen} modal={modal}>
      {modal && <PopoverAnchor virtualRef={viewportAnchor} />}
      <PopoverTrigger asChild>
        <Button
          ref={triggerRef}
          variant="outline"
          role="combobox"
          aria-expanded={disabled ? false : open}
          disabled={disabled}
          onPointerDown={event => {
            openedByTouch.current = event.pointerType === 'touch';
          }}
          onKeyDown={() => {
            openedByTouch.current = false;
          }}
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
      <PopoverContent
        ref={contentRef}
        className="flex max-h-[var(--radix-popover-content-available-height)] w-[max(var(--radix-popover-trigger-width),12rem)] flex-col p-0"
        collisionPadding={8}
        onOpenAutoFocus={event => {
          if (openedByTouch.current) {
            // Keep the options visible on phones instead of opening the
            // software keyboard. Search is still available with a tap.
            event.preventDefault();
            contentRef.current?.focus({ preventScroll: true });
          }
        }}>
        <Command
          className="min-h-0 [&_[cmdk-input-wrapper]]:shrink-0"
          filter={(itemValue, search) => {
            if (itemValue === ACTION_VALUE) return 1;
            const matchedOption = options.find(opt => opt.value === itemValue);
            const textToSearch = matchedOption ? matchedOption.label : itemValue;
            return textToSearch.toLowerCase().includes(search.toLowerCase()) ? 1 : 0;
          }}>
          <CommandInput placeholder="Search..." />
          <CommandList className="min-h-0 flex-1 overscroll-contain">
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

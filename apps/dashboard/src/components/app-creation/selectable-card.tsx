import React from 'react';

type SelectableCardProps = {
  selected: boolean;
  onSelect: () => void;
  control: 'radio' | 'checkbox';
  name?: string;
  value?: string;
  disabled?: boolean;
  children: React.ReactNode;
};

// SelectableCard is the bordered radio/checkbox row used across the modal for
// keys modes, app picking and the history opt-in.
export const SelectableCard = ({
  selected,
  onSelect,
  control,
  name,
  value,
  disabled,
  children,
}: SelectableCardProps) => (
  <label
    className={`flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors ${
      selected
        ? 'bg-accent/40 border-foreground/30 text-foreground'
        : 'bg-background/50 border-border text-muted-foreground hover:bg-accent/20'
    }`}>
    <input
      type={control}
      name={name}
      value={value}
      checked={selected}
      onChange={onSelect}
      disabled={disabled}
      className="mt-0.5 accent-primary h-4 w-4"
    />
    <div className="flex min-w-0 flex-col gap-0.5">{children}</div>
  </label>
);

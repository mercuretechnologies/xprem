// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { LucideIcon } from 'lucide-react';

// The full-panel message a view shows instead of its content: nothing to draw
// yet, or the read failed. Written once because the three error panels had
// drifted into three heights and two typographies, which reads as three
// different kinds of failure when it is the same one.
//
// Only for the views whose whole surface is replaced. Logs and Devices show
// their empty state inside the table, under the column headers, which is a
// different thing and stays where it is.
export const ObserveNotice = ({
  icon: Icon,
  tone = 'muted',
  title,
  detail,
}: {
  icon: LucideIcon;
  // 'error' is the read that failed, and is the only one that borrows the
  // destructive colour; 'muted' is the ordinary "nothing here yet".
  tone?: 'muted' | 'error';
  title: string;
  detail?: string;
}) => (
  <div
    className={`flex min-h-80 flex-col items-center justify-center rounded-xl border bg-card px-6 text-center ${
      tone === 'error' ? '' : 'border-dashed'
    }`}>
    <Icon
      className={`h-6 w-6 ${tone === 'error' ? 'text-destructive' : 'text-muted-foreground'}`}
    />
    <h2 className="mt-3 text-sm font-medium">{title}</h2>
    {detail && (
      <p className="mt-1 max-w-md text-xs leading-relaxed text-muted-foreground">{detail}</p>
    )}
  </div>
);

import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button } from '@/components/ui/button';

// The dashboard is a long-lived tab and its routes are code-split, so a deploy
// while someone has it open removes the chunk their next click asks for. The
// import rejects, lazy rethrows during render, and with nothing to catch it
// React unmounts the whole tree: a blank page across the entire app, not just
// the route that failed. This turns that into a message with a way out.
//
// Class component because error boundaries have no hook equivalent.
type Props = { children: ReactNode };
type State = { failed: boolean };

export class RouteErrorBoundary extends Component<Props, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Route failed to render', error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="flex min-h-[60vh] flex-col items-center justify-center gap-3 px-6 text-center">
        <p className="text-sm font-medium">This page could not be loaded.</p>
        <p className="max-w-md text-xs leading-relaxed text-muted-foreground">
          It usually means the server was updated while this tab stayed open. Reloading picks up the
          new version.
        </p>
        <Button size="sm" onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    );
  }
}

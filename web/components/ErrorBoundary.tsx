"use client";

// ErrorBoundary keeps one failing panel from taking the page down. It catches errors
// thrown while rendering the panels below it (not errors in event handlers or async
// code), shows a fallback with a Retry button, and clears itself when `resetKey`
// changes, so a bad view does not keep the panel broken after a good one arrives.
// React already reports a caught error through console.error, so this does not log.

import { Component, type ReactNode } from "react";

type Props = { children: ReactNode; resetKey?: unknown };
type State = { failed: boolean };

export class ErrorBoundary extends Component<Props, State> {
  state: State = { failed: false };
  // The key in force when the child threw. A new bad view changes the key in the very update
  // that throws; comparing against this, not against the previous props, keeps that update from
  // clearing the fallback and rendering the child a second time.
  private failedKey: unknown;

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(): void {
    this.failedKey = this.props.resetKey;
  }

  componentDidUpdate(): void {
    if (this.state.failed && !Object.is(this.failedKey, this.props.resetKey)) {
      this.setState({ failed: false });
    }
  }

  render(): ReactNode {
    if (!this.state.failed) {
      return this.props.children;
    }
    return (
      <div role="alert" className="boundary-failed">
        <p>This panel failed to render.</p>
        <button type="button" onClick={() => this.setState({ failed: false })}>
          Retry
        </button>
      </div>
    );
  }
}

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ErrorBoundary } from "./ErrorBoundary";

function Bomb({ explode }: { explode: boolean }) {
  if (explode) {
    throw new Error("render failed");
  }
  return <p>fine</p>;
}

describe("ErrorBoundary", () => {
  let errors: ReturnType<typeof vi.spyOn>;
  // React reports a caught render error through console.error; keep it out of the output.
  beforeEach(() => {
    errors = vi.spyOn(console, "error").mockImplementation(() => {});
  });
  afterEach(() => errors.mockRestore());

  it("shows the fallback instead of the failing child and still renders its siblings", () => {
    render(
      <div>
        <ErrorBoundary>
          <Bomb explode />
        </ErrorBoundary>
        <ErrorBoundary>
          <Bomb explode={false} />
        </ErrorBoundary>
      </div>,
    );
    expect(screen.getAllByText("This panel failed to render.")).toHaveLength(1);
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("fine")).toBeInTheDocument();
    expect(errors).toHaveBeenCalled(); // React reported it
  });

  it("Retry renders the child again, and a child that still fails shows the fallback again", async () => {
    const user = userEvent.setup();
    let explode = true;
    function Flaky() {
      return <Bomb explode={explode} />;
    }
    render(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    );
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(screen.getByText("This panel failed to render.")).toBeInTheDocument(); // still failing
    explode = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(screen.getByText("fine")).toBeInTheDocument();
    expect(screen.queryByText("This panel failed to render.")).not.toBeInTheDocument();
  });

  it("clears itself when the reset key changes, by identity, even if the content looks the same", () => {
    const a = { seq: 7 };
    const sameSeqDifferentObject = { seq: 7 };
    const { rerender } = render(
      <ErrorBoundary resetKey={a}>
        <Bomb explode />
      </ErrorBoundary>,
    );
    expect(screen.getByText("This panel failed to render.")).toBeInTheDocument();
    rerender(
      <ErrorBoundary resetKey={a}>
        <Bomb explode={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText("This panel failed to render.")).toBeInTheDocument(); // same key: stays failed
    rerender(
      <ErrorBoundary resetKey={sameSeqDifferentObject}>
        <Bomb explode={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText("fine")).toBeInTheDocument();
  });

  it("a new key that arrives with a throwing child costs one failed render, not two", () => {
    let renders = 0;
    function Counting({ explode }: { explode: boolean }) {
      renders++;
      return <Bomb explode={explode} />;
    }
    // Baseline: the child throws on a re-render with the key unchanged.
    const base = render(
      <ErrorBoundary resetKey={1}>
        <Counting explode={false} />
      </ErrorBoundary>,
    );
    renders = 0;
    base.rerender(
      <ErrorBoundary resetKey={1}>
        <Counting explode />
      </ErrorBoundary>,
    );
    const unchangedKey = renders;
    base.unmount();
    // A good view, then a bad one: the key changes in the update that throws.
    const next = render(
      <ErrorBoundary resetKey={1}>
        <Counting explode={false} />
      </ErrorBoundary>,
    );
    renders = 0;
    errors.mockClear();
    next.rerender(
      <ErrorBoundary resetKey={2}>
        <Counting explode />
      </ErrorBoundary>,
    );
    expect(renders).toBe(unchangedKey);
    expect(screen.getByText("This panel failed to render.")).toBeInTheDocument();
  });

  it("does not grow its error reports when it re-renders while failed", () => {
    const { rerender } = render(
      <ErrorBoundary resetKey={1}>
        <Bomb explode />
      </ErrorBoundary>,
    );
    const after = errors.mock.calls.length;
    expect(after).toBeGreaterThan(0);
    rerender(
      <ErrorBoundary resetKey={1}>
        <Bomb explode />
      </ErrorBoundary>,
    );
    expect(errors.mock.calls.length).toBe(after);
  });
});

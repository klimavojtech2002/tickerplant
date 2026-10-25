import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { Footer } from "./Footer";

describe("Footer", () => {
  it("says prices and sizes are the exchange's exact decimals, and where the page is connected", () => {
    render(<Footer edgeURL="http://127.0.0.1:8080" version="v1" />);
    expect(screen.getByText("Prices and sizes are the exchange's exact decimals, never rounded.")).toBeInTheDocument();
    expect(screen.getByText("Server address: http://127.0.0.1:8080")).toBeInTheDocument();
    expect(screen.getByText("Server version v1")).toBeInTheDocument();
  });

  it("leaves the version out until the server has given one", () => {
    render(<Footer edgeURL="http://edge" version={null} />);
    expect(screen.queryByText(/Server version/)).not.toBeInTheDocument();
  });

  it("has About closed to begin with, with its text in the page but not shown", () => {
    const { container } = render(<Footer edgeURL="http://edge" version="v1" />);
    const button = screen.getByRole("button", { name: "About this page" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveAttribute("aria-controls", "about-panel");
    expect(container.querySelector("#about-panel")).toHaveAttribute("hidden");
    expect(screen.queryByText(/rebuilds an exchange's order book/)).not.toBeVisible();
  });

  it("opens and closes with a click, and its target exists", async () => {
    const user = userEvent.setup();
    const { container } = render(<Footer edgeURL="http://edge" version="v1" />);
    await user.click(screen.getByRole("button", { name: "About this page" }));
    expect(screen.getByRole("button", { name: "About this page" })).toHaveAttribute("aria-expanded", "true");
    expect(container.querySelector("#about-panel")).not.toHaveAttribute("hidden");
    expect(screen.getByText(/rebuilds an exchange's order book/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "About this page" }));
    expect(container.querySelector("#about-panel")).toHaveAttribute("hidden");
  });

  it("opens and closes with Enter and with Space, and keeps the focus on the button", async () => {
    const user = userEvent.setup();
    render(<Footer edgeURL="http://edge" version="v1" />);
    const button = screen.getByRole("button", { name: "About this page" });
    button.focus();
    await user.keyboard("{Enter}");
    expect(button).toHaveAttribute("aria-expanded", "true");
    await user.keyboard(" ");
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveFocus();
  });

  it("tells the reader how to read the cards", async () => {
    const user = userEvent.setup();
    render(<Footer edgeURL="http://edge" version="v1" />);
    await user.click(screen.getByRole("button", { name: "About this page" }));
    expect(
      screen.getByText(/The mid price is the middle of the best bid and the best ask, not the price of a trade/),
    ).toBeInTheDocument();
    expect(screen.getByText(/leave out the network/)).toBeInTheDocument();
  });

  it("has no link to a source, since the repository may not be public", () => {
    render(<Footer edgeURL="http://edge" version="v1" />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("the stylesheet", () => {
  const css = readFileSync(join(__dirname, "..", "app", "globals.css"), "utf8");

  it("styles the failed-panel fallback of the error boundary, which sits outside any older scope", () => {
    expect(css).toMatch(/\.boundary-failed\s*\{/);
  });

  /** The text between the braces of the first `@media print` block. */
  function printBlock(): string {
    const start = css.indexOf("@media print");
    let depth = 0;
    for (let i = css.indexOf("{", start); i < css.length; i++) {
      if (css[i] === "{") depth++;
      if (css[i] === "}" && --depth === 0) {
        return css.slice(css.indexOf("{", start) + 1, i);
      }
    }
    throw new Error("unterminated print block");
  }
  const escape = (selector: string) => selector.replace(/[.[\]]/g, (c) => "\\" + c);

  it("opens the About text and hides what only works on a screen when printed", () => {
    const print = printBlock();
    expect(print).toMatch(/\.about-panel\[hidden\]\s*\{[^}]*display:\s*block\s*!important/);
    for (const hidden of [".mk-switch", ".nav-right", ".pause-btn", ".log-toggle", ".about-btn", ".chart-tip"]) {
      expect(print, hidden).toMatch(new RegExp(`${escape(hidden)}\s*[,{]`));
    }
    expect(print).toMatch(/\.mk-grid\[data-shown\] \.mk-card\s*\{\s*display:\s*flex\s*!important/);
  });

  it("prints in the light colours whatever theme was chosen, by setting the tokens again above both dark blocks", () => {
    const print = printBlock();
    const block = /:root:root:root\s*\{([^}]*)\}/.exec(print);
    expect(block, "a :root:root:root block in print").not.toBeNull();
    for (const token of ["--bg: #fff", "--card: #fff", "--label: #000"]) {
      expect(block![1]).toContain(token);
    }
  });
});

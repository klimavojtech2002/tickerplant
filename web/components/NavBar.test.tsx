import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { initialConnection } from "@/lib/connection";
import { themeKey } from "@/lib/theme";

import { NavBar } from "./NavBar";

describe("NavBar", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });
  afterEach(() => vi.restoreAllMocks());

  it("shows the name, the connection pill and the appearance control, in a header landmark", () => {
    render(<NavBar conn={initialConnection} />);
    expect(screen.getByRole("banner")).toBeInTheDocument();
    expect(screen.getByText("tickerplant")).toBeInTheDocument();
    expect(screen.getByText("Connecting…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /appearance/i })).toBeInTheDocument();
  });

  it("cycles system, light, dark, system, applying and remembering each explicit choice", async () => {
    const user = userEvent.setup();
    render(<NavBar conn={initialConnection} />);
    const button = () => screen.getByRole("button", { name: /appearance/i });
    expect(button()).toHaveAccessibleName("Appearance: follows the system. Switch to light.");

    await user.click(button());
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    expect(localStorage.getItem(themeKey)).toBe("light");
    expect(button()).toHaveAccessibleName("Appearance: light. Switch to dark.");

    await user.click(button());
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(localStorage.getItem(themeKey)).toBe("dark");

    await user.click(button());
    expect(document.documentElement).not.toHaveAttribute("data-theme");
    expect(localStorage.getItem(themeKey)).toBeNull();
    expect(button()).toHaveAccessibleName("Appearance: follows the system. Switch to light.");
  });

  it("starts from a saved choice, read after mount", async () => {
    localStorage.setItem(themeKey, "dark");
    render(<NavBar conn={initialConnection} />);
    expect(
      await screen.findByRole("button", { name: "Appearance: dark. Switch to follow the system." }),
    ).toBeInTheDocument();
  });

  it("still works, for this visit, when storage throws", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const user = userEvent.setup();
    render(<NavBar conn={initialConnection} />);
    await user.click(screen.getByRole("button", { name: /appearance/i }));
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });

  it("can be reached and used from the keyboard", async () => {
    const user = userEvent.setup();
    render(<NavBar conn={initialConnection} />);
    await user.tab();
    expect(screen.getByRole("button", { name: /appearance/i })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });
});

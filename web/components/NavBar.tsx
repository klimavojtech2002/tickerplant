import type { ConnectionState } from "@/lib/connection";
import { copy } from "@/lib/copy";

import { LinkPill } from "./LinkPill";
import { ThemeToggle } from "./ThemeToggle";

export function NavBar({ conn }: { conn: ConnectionState }) {
  return (
    <header className="nav">
      <div className="nav-in">
        <span className="wordmark">{copy.appName}</span>
        <div className="nav-right">
          <LinkPill conn={conn} />
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}

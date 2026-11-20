import { ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { notifyHost } from "./host";

/** How long the cursor shows, and then how long it is hidden. */
const half = 600;

/** Hides the cursor layer. See style.css. */
const offClass = "sl-cursor-off";

const root = document.documentElement;
let steady = false;
let focused = false;
let timer: number | undefined;

/**
 * Shows the cursor and starts the blink again from there, so the cursor
 * stays in view while the user types or moves it.
 */
function restart(): void {
  clearInterval(timer);
  timer = undefined;
  root.classList.remove(offClass);
  if (steady || !focused) return;
  timer = window.setInterval(() => {
    // The host reads this before the frame that the change makes.
    notifyHost("blink");
    root.classList.toggle(offClass);
  }, half);
}

/** Turns the blink off or on. */
export function setSteady(on: boolean): void {
  steady = on;
  restart();
}

/**
 * Blinks the cursor while the editor has focus. CodeMirror's own blink is a
 * CSS animation, which gives the host no word of a frame that differs from
 * the last only in the cursor.
 */
export const blink = ViewPlugin.fromClass(
  class {
    update(update: ViewUpdate): void {
      if (!update.focusChanged && !update.selectionSet && !update.docChanged) return;
      focused = update.view.hasFocus;
      restart();
    }
    destroy(): void {
      focused = false;
      restart();
    }
  },
);

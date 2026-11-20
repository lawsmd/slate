import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";

/** The fewest pixels the thumb is tall, so it stays visible beside long text. */
const minThumb = 16;

/**
 * Draws a scrollbar at the right edge of the editor while the text is taller
 * than the editor. The browser runs with its own scrollbars hidden, and a
 * bar drawn here takes the theme's colours. It shows where the view is and
 * does not take the mouse.
 */
export const scrollbar = ViewPlugin.fromClass(
  class {
    track: HTMLElement;
    thumb: HTMLElement;
    resize: ResizeObserver;
    onScroll: () => void;

    constructor(readonly view: EditorView) {
      this.track = document.createElement("div");
      this.track.className = "sl-scrollbar";
      this.track.setAttribute("aria-hidden", "true");
      this.thumb = document.createElement("div");
      this.thumb.className = "sl-scrollbar-thumb";
      this.track.appendChild(this.thumb);
      view.dom.appendChild(this.track);
      this.onScroll = () => this.place();
      view.scrollDOM.addEventListener("scroll", this.onScroll, { passive: true });
      this.resize = new ResizeObserver(this.onScroll);
      this.resize.observe(view.scrollDOM);
      this.resize.observe(view.contentDOM);
      this.place();
    }

    update(update: ViewUpdate): void {
      if (update.docChanged || update.geometryChanged || update.viewportChanged) this.place();
    }

    place(): void {
      const { scrollTop, scrollHeight, clientHeight } = this.view.scrollDOM;
      if (scrollHeight <= clientHeight + 1) {
        this.track.style.display = "none";
        return;
      }
      this.track.style.display = "";
      const track = this.track.clientHeight || clientHeight;
      const height = Math.max(minThumb, Math.round((track * clientHeight) / scrollHeight));
      const top = Math.round(((track - height) * scrollTop) / (scrollHeight - clientHeight));
      this.thumb.style.height = `${height}px`;
      this.thumb.style.transform = `translateY(${top}px)`;
    }

    destroy(): void {
      this.view.scrollDOM.removeEventListener("scroll", this.onScroll);
      this.resize.disconnect();
      this.track.remove();
    }
  },
);

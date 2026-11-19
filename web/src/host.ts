// Messages between the page and the Go host. The host installs
// `window.slateHost` as a DevTools binding and answers requests by calling
// `slate.resolve`.

type HostFn = (message: string) => void;

const pending = new Map<number, (value: unknown) => void>();
let sequence = 0;

function hostFn(): HostFn | undefined {
  return (window as unknown as { slateHost?: HostFn }).slateHost;
}

/** Tells the host something happened. */
export function notifyHost(type: string, payload: Record<string, unknown> = {}): void {
  hostFn()?.(JSON.stringify({ type, ...payload }));
}

/** Asks the host a question. Resolves to null without a host or an answer. */
export function requestHost<T>(type: string, payload: Record<string, unknown>, timeoutMs = 5000): Promise<T | null> {
  const host = hostFn();
  if (!host) return Promise.resolve(null);
  const id = ++sequence;
  return new Promise((resolve) => {
    pending.set(id, resolve as (value: unknown) => void);
    host(JSON.stringify({ type, id, ...payload }));
    setTimeout(() => {
      if (pending.delete(id)) resolve(null);
    }, timeoutMs);
  });
}

export function resolveHost(id: number, value: unknown): void {
  const resolve = pending.get(id);
  if (resolve) {
    pending.delete(id);
    resolve(value);
  }
}

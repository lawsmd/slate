import type { CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import { syntaxTree } from "@codemirror/language";
import { mentionScheme } from "./syntax";
import { requestHost } from "./host";

export interface MentionUser {
  id: string;
  name: string;
}

/** Completes `@name` from the host's user search into a mention link. */
export async function mentionSource(context: CompletionContext): Promise<CompletionResult | null> {
  // One space is allowed so that a full name can be typed.
  const match = context.matchBefore(/@[\w.-]*(?: [\w.-]*)?/);
  if (!match) return null;
  if (match.from > 0 && /\w/.test(context.state.sliceDoc(match.from - 1, match.from))) return null; // an email address
  for (let n = syntaxTree(context.state).resolveInner(match.from, 1); n.parent; n = n.parent) {
    if (/Code/.test(n.name)) return null;
  }
  const users = await requestHost<MentionUser[]>("mentions", { query: match.text.slice(1) });
  if (!users?.length || context.aborted) return null;
  return {
    from: match.from,
    filter: false,
    options: users.map((u) => ({
      label: "@" + u.name,
      apply: `[@${u.name}](${mentionScheme}${encodeURIComponent(u.id)}) `,
    })),
  };
}

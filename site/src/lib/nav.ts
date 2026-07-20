import { getCollection, type CollectionEntry } from "astro:content";

export type DocEntry = CollectionEntry<"docs">;

/** All docs, flattened and sorted by `order` — the prev/next sequence. */
export async function orderedDocs(): Promise<DocEntry[]> {
  const docs = await getCollection("docs");
  return docs.sort((a, b) => a.data.order - b.data.order);
}

/**
 * Group ordered docs by `group`. Because input is order-sorted and Map
 * preserves insertion order, groups come out ordered by the minimum
 * `order` they contain (per spec).
 */
export function groupDocs(docs: DocEntry[]): [string, DocEntry[]][] {
  const groups = new Map<string, DocEntry[]>();
  for (const doc of docs) {
    const list = groups.get(doc.data.group) ?? [];
    list.push(doc);
    groups.set(doc.data.group, list);
  }
  return [...groups.entries()];
}

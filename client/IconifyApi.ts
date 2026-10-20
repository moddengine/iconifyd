import type { ExtendedIconifyIcon, IconifyJSON } from "@iconify/types";
import { getIconData } from "@iconify/utils/lib/icon-set/get-icon";

export interface IconSearchResult {
  icons: string[];
  total: number;
  available: number;
  limit: number;
  start: number;
}

export interface IconCollection {
  name: string;
  total: number;
  palette: boolean;
  license: { spdx: string };
}

export function createIconifyClient(baseURL: string) {
  const api = baseURL.replace(/\/$/, "");
  async function getJSON<T>(path: string): Promise<T> {
    const response = await fetch(`${api}/${path}`);
    if (!response.ok) {
      throw new Error(`Iconify API returned ${response.status}`);
    }
    return response.json() as Promise<T>;
  }

  function searchIcons(
    collection: string,
    query: string,
    limit: number,
    start: number,
  ): Promise<IconSearchResult> {
    const parameters = new URLSearchParams({
      prefix: collection,
      query,
      limit: String(limit),
      start: String(start),
    });
    return getJSON(`search?${parameters}`);
  }

  function loadCollections(): Promise<Record<string, IconCollection>> {
    return getJSON("collections");
  }

  async function loadIcons(
    collection: string,
    names: string[],
  ): Promise<Map<string, ExtendedIconifyIcon>> {
    if (names.length === 0) return new Map();
    const parameters = new URLSearchParams({ icons: names.join(",") });
    const iconSet = await getJSON<IconifyJSON>(
      `${encodeURIComponent(collection)}.json?${parameters}`,
    );
    const icons = new Map<string, ExtendedIconifyIcon>();
    names.forEach((name) => {
      const icon = getIconData(iconSet, name);
      if (icon) icons.set(name, icon);
    });
    return icons;
  }

  async function loadIcon(
    collection: string,
    name: string,
  ): Promise<ExtendedIconifyIcon> {
    const icon = (await loadIcons(collection, [name])).get(name);
    if (!icon) throw new Error(`Icon not found ${collection}/${name}`);
    return icon;
  }

  return { searchIcons, loadCollections, loadIcons, loadIcon };
}

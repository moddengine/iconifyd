import { afterEach, expect, it, vi } from "vitest";
import { createIconifyClient } from "./IconifyApi";
const { loadCollections, loadIcon, searchIcons } = createIconifyClient(
  "https://icons.example.test/",
);

afterEach(() => vi.unstubAllGlobals());

it("searches and loads icons from the bundled API", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({ mdi: { name: "Material Design Icons" } }),
    })
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        icons: ["mdi:home"],
        total: 1,
        available: 1,
        limit: 100,
        start: 0,
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        prefix: "mdi",
        icons: { home: { body: "<path/>", width: 24, height: 24 } },
      }),
    });
  vi.stubGlobal("fetch", fetchMock);

  const collections = await loadCollections();
  const result = await searchIcons("mdi", "home icon", 100, 0);
  const icon = await loadIcon("mdi", "home");

  expect(collections.mdi.name).toBe("Material Design Icons");
  expect(result.icons).toEqual(["mdi:home"]);
  expect(icon).toMatchObject({ body: "<path/>", width: 24, height: 24 });
  expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
    "https://icons.example.test/collections",
    "https://icons.example.test/search?prefix=mdi&query=home+icon&limit=100&start=0",
    "https://icons.example.test/mdi.json?icons=home",
  ]);
});

it("reports HTTP errors and missing icons", async () => {
  const client = createIconifyClient("https://icons.example.test");
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 503 }));
  await expect(client.loadCollections()).rejects.toThrow("503");
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ prefix: "mdi", icons: {} }),
    }),
  );
  await expect(client.loadIcon("mdi", "missing")).rejects.toThrow(
    "Icon not found mdi/missing",
  );
});

it("does not request an empty icon list", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  expect(await createIconifyClient("").loadIcons("mdi", [])).toEqual(new Map());
  expect(fetch).not.toHaveBeenCalled();
});

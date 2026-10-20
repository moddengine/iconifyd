import assert from "node:assert/strict";

const base = process.argv[2] || "http://127.0.0.1:8080";
let ready = false;
for (let attempt = 0; attempt < 60; attempt++) {
  try {
    if ((await fetch(`${base}/collections`)).ok) {
      ready = true;
      break;
    }
  } catch {}
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
assert(ready, "server did not become ready");
const result = await fetch(`${base}/search?query=home&limit=32`);
assert.equal(result.status, 200);
const search = await result.json();
assert(search.available > 0);
const [collection, name] = search.icons[0].split(":");
const data = await fetch(`${base}/${collection}.json?icons=${name}`);
assert.equal(data.status, 200);
assert((await data.json()).icons[name]);
const svg = await fetch(`${base}/${collection}/${name}.svg`);
assert.equal(svg.status, 200);
assert((await svg.text()).includes("<svg"));
console.log("Full-corpus HTTP smoke test passed");

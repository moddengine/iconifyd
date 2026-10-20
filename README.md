# iconifyd

A standalone Go Iconify server, compressed SQLite asset builder, and TypeScript
client extracted from ModdEngine. The Go handler can also be embedded directly
in another Go application.

## Run the server

Each successful build of `main` publishes a GitHub release `v0.1.<run number>`
and matching AMD64/ARM64 images:

- `ghcr.io/moddengine/iconifyd:0.1.<run number>`: HTTP server with bundled database.
- `ghcr.io/moddengine/iconifyd-data:0.1.<run number>`: database and metadata only.

Use a published version from the Releases page:

```sh
docker run --rm -p 8080:8080 ghcr.io/moddengine/iconifyd:0.1.1
```

The server runs as UID/GID 65532 and listens on `:8080`. Override flags after
the image name: `serve -listen :8081 -db /custom/icons.sqlite`. The default
SQLite path is `/usr/share/iconifyd/icons.sqlite`.

Endpoints:

```text
GET /{prefix}.json?icons={name,name,...}
GET /{prefix}/{name}.svg
GET /symbol/{prefix}/{name}.svg
GET /search?query={keyword}&prefix={collection}&limit=64&start=0
GET /collections
GET /version
```

Search also accepts `prefixes` and `category`. `/version` identifies the API
protocol, not the asset release; `manifest.json` identifies the source icon
version, builder revision, and schema version. The source corpus is locked in
`package-lock.json`. SQLite uses 32 KiB pages and an 8 KiB zstd dictionary. Icons or aliases with
non-positive dimensions, and aliases whose canonical icon was rejected, are
excluded with a warning during the build. Other malformed collection data
still fails the build.

To embed the database in ModdEngine without rebuilding it:

```dockerfile
FROM ghcr.io/moddengine/iconifyd-data:0.1.1 AS icons
# In your runtime stage:
COPY --from=icons /usr/share/iconifyd/icons.sqlite /usr/share/moddengine/iconify.sqlite
```

Pin published image digests for reproducible builds. Dependabot can propose
version updates to these `FROM` references. ModdEngine's existing code and
Dockerfile have not been changed by this extraction.

## Build locally

Requires Go 1.25 or newer with CGO and a C compiler, Node.js 24, npm, and zstd:

```sh
npm ci --ignore-scripts
npm test
npm run build
go test -mod=mod -race ./...
go build -mod=mod -o iconifyd ./cmd/iconifyd
./iconifyd pack node_modules/@iconify/json/json icons.sqlite
./iconifyd serve -db icons.sqlite
```

Or generate release assets and build the server entirely in Docker:

```sh
docker build --target assets --output type=local,dest=dist .
docker build --target server -t iconifyd:local .
docker run --rm -p 8080:8080 iconifyd:local
```

`pkg/iconifyapi.New(path)` returns an `http.Handler`; call `Close()` on shutdown.
It also retains the `GetSVG` method and request/response types used by
ModdEngine's RPC integration. The standalone HTTP server does not expose an RPC
listener.

## TypeScript client

The client is independent of Vue and ModdEngine. Build with `npm run build`;
compiled JavaScript and declarations are also attached to releases as
`client.tar.gz`. It requires `@iconify/utils`, `@iconify/types`, and standard
`fetch`. It is not published to npm.

```ts
import { createIconifyClient } from "./client/IconifyApi.js";

const icons = createIconifyClient("https://icons.example.com");
const results = await icons.searchIcons("mdi", "home", 64, 0);
const home = await icons.loadIcon("mdi", "home");
```

Use an empty base URL for same-origin root endpoints, or supply a URL with a
path prefix when reverse proxying. Vue components and PHP integration remain
in ModdEngine.

## CI and releases

Pull requests run formatting checks, TypeScript tests/build, Go race tests and
vet, generate the entire corpus, and smoke-test HTTP search, JSON and SVG
responses in the non-root server container. A successful `main` build publishes
both images and a GitHub release containing SQLite, provenance metadata,
collection metadata, client build, upstream notices and SHA-256 checksums.
Workflow reruns reuse the same version; a new run gets a new version. Version
numbers may have gaps. Change the `0.1` prefix in the workflow when changing the
public compatibility contract. Breaking schema changes must update
`schemaVersion` and coordinate with database consumers.

Dependabot checks the pinned `@iconify/json` dependency daily; merging its PR
builds and releases updated assets. Go modules, GitHub Actions and base images
are checked weekly. There is no custom upstream polling or automatic merge.
The workflow uses `GITHUB_TOKEN` for GHCR and releases; no personal token or
Docker Hub credentials are required. Organization policy must allow Actions
and package publishing. GHCR package visibility is managed separately from
repository visibility; grant consumer repositories read access or make the
packages public as appropriate.

Enable the dependency graph, Dependabot alerts and Dependabot security updates
in repository settings. Version update PRs track new icon data; security alerts
track known vulnerabilities in package dependencies, not the generated SQLite
file. See [NOTICE.md](NOTICE.md) for code and icon licensing.

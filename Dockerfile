# syntax=docker/dockerfile:1
FROM node:26.12.0-trixie-slim AS corpus
WORKDIR /source
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
ARG REVISION=development
RUN node --input-type=module -e 'import fs from "node:fs"; const pkg = JSON.parse(fs.readFileSync("node_modules/@iconify/json/package.json")); fs.writeFileSync("manifest.json", JSON.stringify({iconifyVersion:pkg.version,schemaVersion:1,builderRevision:process.env.REVISION},null,2)+"\n")'

FROM golang:1.27.2-trixie AS build
WORKDIR /source
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /iconifyd ./cmd/iconifyd

FROM build AS database
RUN apt-get update && apt-get install -y --no-install-recommends zstd && rm -rf /var/lib/apt/lists/*
COPY --from=corpus /source/node_modules/@iconify/json/json /json
RUN mkdir /out && /iconifyd pack /json /out/icons.sqlite
COPY --from=corpus /source/manifest.json /out/manifest.json
COPY --from=corpus /source/node_modules/@iconify/json/collections.json /out/collections.json
COPY --from=corpus /source/node_modules/@iconify/json/readme.md /out/ICONIFY-README.md

FROM scratch AS assets
COPY --from=database /out/ /

FROM scratch AS data
LABEL org.opencontainers.image.source="https://github.com/moddengine/iconifyd"
COPY dist/icons.sqlite dist/manifest.json dist/collections.json dist/ICONIFY-README.md /usr/share/iconifyd/

FROM debian:trixie-slim AS server
LABEL org.opencontainers.image.source="https://github.com/moddengine/iconifyd"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /iconifyd /usr/local/bin/iconifyd
COPY dist/icons.sqlite dist/manifest.json dist/collections.json dist/ICONIFY-README.md /usr/share/iconifyd/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/iconifyd"]
CMD ["serve"]

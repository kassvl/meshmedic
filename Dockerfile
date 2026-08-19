# MeshMedic ships as a static binary plus the catalog it reads at startup.
# The catalog is baked in at a fixed path and pointed to by MESHMEDIC_CATALOG,
# so the image is correct no matter what working directory it runs from and
# `--catalog` still overrides it for anyone mounting their own.
#
# kubectl is deliberately absent. Configuration and triage evidence degrade to
# a logged line without it, and metric detection is unchanged; adding a
# cluster CLI to the default image would enlarge the trusted surface of a tool
# whose main claim is that it holds no cluster write access.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Dependencies first so a source-only change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/meshmedic ./cmd/meshmedic


FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/meshmedic /usr/local/bin/meshmedic
COPY --chmod=0755 catalog/ /etc/meshmedic/catalog/
# The lock travels with the catalog it locks. Without it every entry is
# unlocked and the image detects nothing at all, which is the correct
# behaviour for an unreviewed catalog and a useless one to ship.
COPY --chmod=0644 catalog.lock /etc/meshmedic/catalog.lock

ENV MESHMEDIC_CATALOG=/etc/meshmedic/catalog

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/meshmedic"]
CMD ["validate"]

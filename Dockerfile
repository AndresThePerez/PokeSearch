# Bases are pinned by digest, not just by tag: a tag is a moving pointer, and
# "it built last week" has to keep meaning something. The build stage is the
# release of go.mod's toolchain line, the one CI builds and scans with, so the
# shipped binary is built with the release the vulnerability scan read. Refresh
# deliberately with
#   docker buildx imagetools inspect golang:1.26
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
# Resolved 2026-11-20.
FROM golang:1.26.11@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build identity, served by GET /api/meta. Defaults match internal/version's,
# so an unstamped build is honestly labelled rather than falsely versioned.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X github.com/AndresThePerez/pokesearch/internal/version.Version=${VERSION} -X github.com/AndresThePerez/pokesearch/internal/version.Commit=${COMMIT} -X github.com/AndresThePerez/pokesearch/internal/version.Built=${BUILT}" \
  -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -o /out/seed ./cmd/seed

# :nonroot runs as uid 65532. Neither binary writes to disk — the seeder
# streams the tarball through memory and the server only reads its embedded FS.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/server /server
COPY --from=build /out/seed /seed
EXPOSE 8080
ENTRYPOINT ["/server"]

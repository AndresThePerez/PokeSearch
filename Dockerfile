# Bases are pinned by digest, not just by tag: a tag is a moving pointer, and
# "it built last week" has to keep meaning something. Refresh deliberately with
#   docker buildx imagetools inspect golang:1.26
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
# Resolved 2026-08-22.
FROM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS build

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

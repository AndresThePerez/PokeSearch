FROM golang:1.26 AS build

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

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
COPY --from=build /out/seed /seed
EXPOSE 8080
ENTRYPOINT ["/server"]

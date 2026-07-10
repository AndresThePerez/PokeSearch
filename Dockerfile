FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -o /out/seed ./cmd/seed

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
COPY --from=build /out/seed /seed
EXPOSE 8080
ENTRYPOINT ["/server"]

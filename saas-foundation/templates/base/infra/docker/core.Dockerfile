# syntax=docker/dockerfile:1
# Core (Go) image. Build context: core/. Targets: check, runtime.
FROM golang:1.27.1-alpine AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -o /out/server ./cmd/server

FROM build AS check
# Formatting, vet and unit tests; fails the build-free check run on any finding.
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { gofmt -l .; exit 1; }; go vet ./... && go test ./..."]

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/server /usr/local/bin/server
USER nonroot:nonroot
EXPOSE 8180
ENTRYPOINT ["/usr/local/bin/server"]

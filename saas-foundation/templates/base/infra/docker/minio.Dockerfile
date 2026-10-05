# syntax=docker/dockerfile:1
# MinIO, built from the final upstream community source releases. Upstream no
# longer publishes maintained images and documents source builds ("clone the
# source and build the latest container"). Local development and validation
# only; the community edition is archived and receives no further fixes.
FROM golang:1.27.1-alpine AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
RUN go install github.com/minio/minio@RELEASE.2025-10-15T17-29-55Z \
 && go install github.com/minio/mc@RELEASE.2025-08-13T08-35-41Z \
 && mkdir -p /out/data /out/home

FROM alpine:3.22 AS init
COPY --from=build /go/bin/mc /usr/bin/mc
LABEL org.opencontainers.image.version="mc RELEASE.2025-08-13T08-35-41Z"
# The init job needs only /tmp; run it unprivileged.
USER 65532:65532

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /go/bin/minio /usr/bin/minio
COPY --from=build /go/bin/mc /usr/bin/mc
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/home /home/nonroot
ENV HOME=/home/nonroot
LABEL org.opencontainers.image.version="minio RELEASE.2025-10-15T17-29-55Z; mc RELEASE.2025-08-13T08-35-41Z"
USER nonroot:nonroot
EXPOSE 9000 9001
ENTRYPOINT ["/usr/bin/minio"]

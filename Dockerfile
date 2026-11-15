# Housekeeparr container image (UNVERIFIED: not yet built in CI or locally).
#
# The image runs headless by default: the web interface binds to loopback
# only and remote exposure requires authentication, which this release does
# not implement. See packaging/README.md.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/housekeeparr ./cmd/housekeeparr

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/housekeeparr /housekeeparr
# /config holds housekeeparr.toml and (by default) the state directory.
VOLUME ["/config"]
ENV HOUSEKEEPARR_CONFIG=/config/housekeeparr.toml
USER nonroot:nonroot
HEALTHCHECK --interval=2m --timeout=10s --start-period=1m CMD ["/housekeeparr", "status"]
ENTRYPOINT ["/housekeeparr"]
CMD ["run", "--headless"]

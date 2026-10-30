FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/flow-breaker-web ./cmd/flow-breaker-web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/web ./cmd/flow-breaker-web && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/web /web
# A new named volume copies this dir's owner, so the nonroot user can write keys.
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8091
ENTRYPOINT ["/web"]

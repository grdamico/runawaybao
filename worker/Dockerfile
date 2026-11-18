ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ internal/
COPY cmd/db-worker/ cmd/db-worker/
COPY cmd/network-probe/ cmd/network-probe/
COPY cmd/demo-child/ cmd/demo-child/
RUN CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/db-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/probe ./cmd/network-probe && \
    CGO_ENABLED=0 go build -trimpath -o /out/child ./cmd/demo-child
FROM scratch
COPY --from=build /out/worker /runawaybao-db-worker
COPY --from=build /out/child /runawaybao-unexpected
COPY --from=build /out/probe /runawaybao-netprobe
USER 65534:65534
ENTRYPOINT ["/runawaybao-db-worker", "bootstrap"]

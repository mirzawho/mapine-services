FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapine ./cmd/api \
    && cp /out/mapine /out/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapine-openapi ./cmd/openapi \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapine-migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapine-backup ./cmd/backup \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/osm ./cmd/osm

FROM alpine:3.22
WORKDIR /bin/mapine
COPY --from=build /out/ /bin/mapine/
COPY --from=build /src/configs/osm/ /bin/mapine/configs/osm/
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY app /volume/app
RUN mkdir -p /volume/public /volume/data && chown -R 65532:65532 /volume
USER 65532:65532
VOLUME ["/volume"]
ENV HTTP_HOST=0.0.0.0 HTTP_PORT=3000 \
    OSM_DATA_DIR=/volume/data/osm \
    OSM_DB_PATH=/volume/data/mapine.db
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:3000/healthz || exit 1
ENTRYPOINT ["/bin/mapine/api"]

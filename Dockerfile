FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapine ./cmd/api

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/mapine /mapine
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY app /volume/app
RUN mkdir -p /volume/public /volume/data && chown -R 65532:65532 /volume
USER 65532:65532
VOLUME ["/volume"]
ENV HTTP_HOST=0.0.0.0 HTTP_PORT=3000
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:3000/healthz || exit 1
ENTRYPOINT ["/mapine"]

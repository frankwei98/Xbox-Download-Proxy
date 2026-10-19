FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/xdp ./cmd/xdp

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/xdp /usr/local/bin/xdp
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/xdp"]
CMD ["serve", "--data-dir", "/data", "--lan-ip", "192.168.1.10"]

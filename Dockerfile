FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /out/anytype-extension-mcp .

FROM scratch

# The server reaches api.unsplash.com over TLS, and a scratch image carries no
# trust store — without these the Go client fails with "certificate signed by
# unknown authority". Everything else it talks to is local: gRPC and the REST
# API on the loopback of the Anytype container.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Shipped inside the image because binaries built here statically link
# anytype-heart, whose license requires that recipients get a copy of it.
COPY LICENSE NOTICE /app/

COPY --from=build /out/anytype-extension-mcp /app/anytype-extension-mcp

ENTRYPOINT ["/app/anytype-extension-mcp"]

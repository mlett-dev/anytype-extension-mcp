FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /out/anytype-extension-mcp .

FROM scratch

COPY --from=build /out/anytype-extension-mcp /app/anytype-extension-mcp

ENTRYPOINT ["/app/anytype-extension-mcp"]

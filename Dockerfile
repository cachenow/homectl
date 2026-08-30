FROM golang:1.27-bookworm AS build
WORKDIR /src
ARG VERSION=dev
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/homectl-server ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/homectl-server /app/homectl-server
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/app/homectl-server"]

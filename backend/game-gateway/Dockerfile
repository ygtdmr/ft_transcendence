# Build with the backend directory as context so the shared module is available:
#   docker build -f game-gateway/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY game-gateway ./game-gateway

WORKDIR /src/game-gateway
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/game-gateway ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/game-gateway /game-gateway

# 8080: public listener (room socket, health). 8081: private listener.
EXPOSE 8080 8081
ENTRYPOINT ["/game-gateway"]

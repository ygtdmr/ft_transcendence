# Build with the backend directory as context so the shared module is available:
#   docker build -f notification-gateway/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY notification-gateway ./notification-gateway

WORKDIR /src/notification-gateway
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/notification-gateway ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/notification-gateway /notification-gateway

# 8080: public listener (notification socket, health). 8081: private listener.
EXPOSE 8080 8081
ENTRYPOINT ["/notification-gateway"]

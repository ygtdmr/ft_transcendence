# Build with the backend directory as context so the shared module is available:
#   docker build -f web-bff/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY web-bff ./web-bff

WORKDIR /src/web-bff
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/web-bff ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/web-bff /web-bff

# 8080: public listener (forwarded REST routes, health).
EXPOSE 8080
ENTRYPOINT ["/web-bff"]

# Build with the backend directory as context so the shared module is available:
#   docker build -f stats-service/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY stats-service ./stats-service

WORKDIR /src/stats-service
RUN CGO_ENABLED=0 go build -trimpath -o /out/stats-service ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/stats-service /stats-service

EXPOSE 8080
ENTRYPOINT ["/stats-service"]

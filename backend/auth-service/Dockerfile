# Build with the backend directory as context so the shared module is available:
#   docker build -f auth-service/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY auth-service ./auth-service

WORKDIR /src/auth-service
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/auth-service ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/auth-service /auth-service

# 8080: public listener (/api/v1/auth, health). 8081: private listener (/internal/v1).
# The schema is not applied here: the deployment runs backend/auth-service/migrations
# once before starting the service.
EXPOSE 8080 8081
ENTRYPOINT ["/auth-service"]

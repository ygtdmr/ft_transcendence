# Build with the backend directory as context so the shared module is available:
#   docker build -f shared/skeleton/Dockerfile .   (run from backend/)
#
# A real service's Dockerfile is the same with its own directory copied and
# built (see backend/shared/SKELETON.md).
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared

WORKDIR /src/shared
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/service ./skeleton/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/service /service

# 8080: public listener. 8081: private listener.
EXPOSE 8080 8081
ENTRYPOINT ["/service"]

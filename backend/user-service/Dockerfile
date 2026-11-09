# Build with the backend directory as context so the shared module is available:
#   docker build -f user-service/Dockerfile .   (run from backend/)
FROM golang:1.27.1 AS build

WORKDIR /src
COPY shared ./shared
COPY user-service ./user-service

WORKDIR /src/user-service
RUN CGO_ENABLED=0 go build -trimpath -o /out/user-service ./cmd/server
RUN mkdir -p /out/avatars

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/user-service /user-service
# A volume mounted here starts with this owner, so the nonroot user can write.
COPY --from=build --chown=65532:65532 /out/avatars /var/lib/user-service/avatars

EXPOSE 8080
ENTRYPOINT ["/user-service"]

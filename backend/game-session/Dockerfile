# Build with the backend directory as context, like every other service:
#   docker build -f game-session/Dockerfile .   (run from backend/)
# Game Session is a standalone module and does not use backend/shared.
FROM golang:1.27.1 AS build

WORKDIR /src
COPY game-session ./game-session

WORKDIR /src/game-session
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/game-session ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/game-session /game-session

# PORT (public) and INTERNAL_PORT (private) are set by the deployment; see .env.example.
# The schema is not applied here: the deployment runs backend/game-session/migrations
# once before starting the service.
EXPOSE 8080 8081
ENTRYPOINT ["/game-session"]

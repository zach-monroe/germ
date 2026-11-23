# Build from the repository root:
#   docker build -f deploy/relay/Dockerfile -t germ-relay .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /germ-relay ./cmd/germ-relay

FROM scratch
COPY --from=build /germ-relay /germ-relay
EXPOSE 8080
ENTRYPOINT ["/germ-relay", "-addr", ":8080"]

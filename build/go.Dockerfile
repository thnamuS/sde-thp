FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN for service in gateway access operations usage worker acme enterprise-worker; do \
      CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/${service} ./cmd/${service}; \
    done

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out /services
CMD ["/services/gateway"]

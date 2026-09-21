FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY main.go auth.go ics.go caldav.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gpnu-schudle .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=build /out/gpnu-schudle /usr/local/bin/gpnu-schudle
ENV KEYS_FILE=/data/keys.json
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gpnu-schudle"]
CMD ["-addr", ":8080"]

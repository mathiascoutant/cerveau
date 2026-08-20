FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cerveau ./cmd/server

FROM alpine:3.20
# ca-certificates : TLS sortant (Mongo Atlas, Slack, Meta, Anthropic, IMAP)
# tzdata          : indispensable, l'assistant raisonne en Europe/Paris
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/cerveau /usr/local/bin/cerveau
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cerveau"]

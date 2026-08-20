FROM golang:1.25-alpine AS build
WORKDIR /src

# Couche de dépendances séparée : elle ne se reconstruit que si go.mod/go.sum
# changent, ce qui rend les builds suivants nettement plus rapides.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cerveau ./cmd/server

FROM alpine:3.20
# ca-certificates : TLS sortant (Mongo Atlas, Slack, Meta, OpenAI, IMAP Gandi)
# tzdata          : indispensable, l'assistant raisonne en Europe/Paris
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 cerveau

COPY --from=build /out/cerveau /usr/local/bin/cerveau

USER cerveau
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cerveau"]

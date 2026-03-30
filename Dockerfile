FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bridge .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bridge /app/bridge
WORKDIR /app
EXPOSE 11970
ENTRYPOINT ["/app/bridge"]

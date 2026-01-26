FROM golang:1.25.5-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

RUN go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

COPY go.mod go.sum ./

RUN go mod download

COPY . .

# Generate OpenAPI client code
WORKDIR /app/src/openAPI
RUN oapi-codegen -config cfg.yaml openapi.yaml
WORKDIR /app

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o static-data-and-migrator ./src

FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add ca-certificates

COPY --from=builder /app/static-data-and-migrator .

CMD ["./static-data-and-migrator"]


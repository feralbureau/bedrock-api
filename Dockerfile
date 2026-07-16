# build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# copy dependency files first for layer caching
# spotify-wrapper must be present before go mod download due to local replace directive
COPY go.mod go.sum ./
COPY spotify-wrapper/go.mod ./spotify-wrapper/
RUN go mod download

# copy source code
COPY . .

# embed build info
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG BUILDTIME=unknown

# build the application
RUN go build -ldflags="-X main.buildVersion=${VERSION} -X main.buildCommit=${COMMIT} -X main.buildTime=${BUILDTIME}" -o bedrock-server ./bedrock_server

# final stage
FROM alpine:3.24

RUN apk add --no-cache ca-certificates

WORKDIR /app

# copy the binary from the builder stage
COPY --from=builder /app/bedrock-server .

# expose grpc and proxy ports
EXPOSE 50052
EXPOSE 8080

# run the server
CMD ["./bedrock-server"]

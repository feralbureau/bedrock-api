# build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# install dependencies
COPY go.mod go.sum ./
RUN go mod download

# copy source code
COPY . .

# build the application
RUN go build -o bedrock-server ./bedrock_server

# final stage
FROM alpine:latest

WORKDIR /app

# copy the binary from the builder stage
COPY --from=builder /app/bedrock-server .

# expose grpc and proxy ports
EXPOSE 50052
EXPOSE 8080

# run the server
CMD ["./bedrock-server"]

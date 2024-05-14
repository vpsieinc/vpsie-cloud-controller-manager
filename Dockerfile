FROM golang:1.21-alpine AS build

RUN apk add --no-cache git

WORKDIR /workspace

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build  -o vpsie-cloud-controller-manager .

FROM alpine:latest
RUN apk add --no-cache ca-certificates

COPY --from=build /workspace/vpsie-cloud-controller-manager /usr/local/bin/vpsie-cloud-controller-manager
ENTRYPOINT ["vpsie-cloud-controller-manager"] 
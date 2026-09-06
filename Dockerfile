FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/kato-bot ./cmd/kato-bot

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kato-bot /kato-bot
USER nonroot:nonroot
ENTRYPOINT ["/kato-bot"]

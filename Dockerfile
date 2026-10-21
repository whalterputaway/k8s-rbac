FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/role-manager ./cmd/role-manager

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/role-manager /app/role-manager
COPY web /app/web
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/role-manager"]

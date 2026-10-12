# Optional container for the failure lab. The pure-Go lab (go test ./lab) needs
# no Docker; this image only packages `sebab-lab` for manual sessions.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/sebab-lab ./lab/cmd/sebab-lab

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sebab-lab /sebab-lab
WORKDIR /lab
ENTRYPOINT ["/sebab-lab", "-out", "/lab/out"]

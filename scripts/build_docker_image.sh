CGO_ENABLED=0 GOOS=linux godep go build -a -installsuffix cgo -o ldr .
docker build -t ld-relay -f Dockerfile .
rm ldr

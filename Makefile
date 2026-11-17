.PHONY: build run test vet docker clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/mywa ./cmd/mywa

run: build
	./bin/mywa

test:
	go test -race ./...

vet:
	go vet ./...

docker:
	docker build -t mywa .

clean:
	rm -rf bin

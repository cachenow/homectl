APP := homectl
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: tidy fmt test vet build server agent release clean

tidy:
	go mod tidy

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

test:
	go test ./...

vet:
	go vet ./...

build: server agent

server:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o bin/homectl-server ./cmd/server

agent:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o bin/homectl-agent ./cmd/agent

release:
	./scripts/build-release.sh $(VERSION)

clean:
	rm -rf bin dist

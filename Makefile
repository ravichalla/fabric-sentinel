BIN := bin/agent
GOFILES := $(shell find . -name '*.go' -not -path './bin/*')

.PHONY: all build test race vet fmt fmt-check check run-sim demo clean

all: check build

build:
	go build -o $(BIN) ./cmd/agent

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@out="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

# Everything CI runs.
check: fmt-check vet race

# Run the agent against simulated links (no RDMA hardware needed).
run-sim: build
	./$(BIN) -mode sim -interval 1s -sim-step 5s

# Scripted end-to-end demo: start the agent in sim mode, watch links change
# state, print what a Prometheus scrape would see.
demo: build
	./hack/demo.sh

clean:
	rm -rf bin

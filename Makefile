.PHONY: build build-release run clean frontend frontend-dev

MAIN = .
BINARY = syncwatch
GO = go

build: frontend
	$(GO) build -o $(BINARY) $(MAIN)

build-release: frontend
	CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(BINARY) $(MAIN)

run: build
	./$(BINARY)

frontend:
	cd web && npx vite build

frontend-dev:
	cd web && npx vite

clean:
	rm -f $(BINARY)
	rm -rf web/dist

test:
	$(GO) test ./...

docker:
	docker build -t syncwatch .

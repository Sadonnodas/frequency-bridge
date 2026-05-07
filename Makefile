.PHONY: dev build ui-build go-build test fmt clean help

help:
	@echo "Frequency Bridge — make targets"
	@echo "  make dev       Run backend (:8080) and Vite dev server (:5173) together"
	@echo "  make build     Build UI, embed into Go binary at bin/freqbridge"
	@echo "  make ui-build  Build UI only, copy into backend/internal/web/dist/"
	@echo "  make go-build  Build Go binary only (uses whatever is in dist/)"
	@echo "  make test      Run backend tests + frontend type/svelte checks"
	@echo "  make fmt       gofmt + prettier"
	@echo "  make clean     Remove build outputs"

dev:
	@echo "Backend on http://localhost:8080  |  Vite dev on http://localhost:5180"
	@echo "(/ws, /api, /healthz on the Vite server proxy through to the backend)"
	@trap 'kill 0' EXIT INT TERM; \
		(cd backend && go run ./cmd/freqbridge) & \
		(cd ui && npm run dev) & \
		wait

ui-build:
	cd ui && npm run build
	rm -rf backend/internal/web/dist
	mkdir -p backend/internal/web/dist
	touch backend/internal/web/dist/.gitkeep
	cp -R ui/build/. backend/internal/web/dist/

go-build:
	mkdir -p bin
	cd backend && go build -trimpath -o ../bin/freqbridge ./cmd/freqbridge

build: ui-build go-build

test:
	cd backend && go test ./...
	cd ui && npm run check

fmt:
	cd backend && go fmt ./...
	cd ui && npm run format

clean:
	rm -rf bin ui/build ui/.svelte-kit
	find backend/internal/web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true

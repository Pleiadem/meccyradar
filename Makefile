.PHONY: test lint build build-windows build-macos smoke

ROOT := $(CURDIR)
FRONTEND := $(ROOT)/frontend
WAILS ?= $(shell command -v wails 2>/dev/null || printf '%s' /Users/qiuqiu/go/bin/wails)
RELEASE_VERSION ?=
VERSION_LDFLAG = $(if $(RELEASE_VERSION),-X meccyradar/internal/buildinfo.Version=$(RELEASE_VERSION),)

test:
	go test ./...

lint:
	go vet ./...
	cd $(FRONTEND) && npx tsc --noEmit

# 默认构建 Windows amd64，便于在 Windows 真机验证。
build: build-windows

build-windows:
	cd $(FRONTEND) && npm run build
	$(WAILS) build -platform windows/amd64 -clean -s -trimpath -ldflags='-H windowsgui -s -w $(VERSION_LDFLAG)' -o meccyradar.exe
	cp build/bin/meccyradar.exe build/meccyradar.exe

# macOS 本机构建保留为显式目标。
build-macos:
	cd $(FRONTEND) && npm run build
	$(WAILS) build -clean -ldflags='$(VERSION_LDFLAG)'

smoke:
	@set -e; \
	printf '%s\n' '[smoke] checking required files'; \
	test -f Design.md; \
	test -f AGENTS.md; \
	test -f DEFINITION_OF_DONE.md; \
	test -f $(FRONTEND)/src/App.tsx; \
	test -f $(FRONTEND)/src/index.css; \
	printf '%s\n' '[smoke] checking required implementation markers'; \
	grep -q 'export default function App' $(FRONTEND)/src/App.tsx; \
	grep -q 'runtime?.WindowMinimise' $(FRONTEND)/src/App.tsx; \
	grep -q 'backend?.CloseWindow' $(FRONTEND)/src/App.tsx; \
	grep -q '授权状态' $(FRONTEND)/src/App.tsx; \
	grep -q '雷达密码' $(FRONTEND)/src/App.tsx; \
	grep -q 'function ConsoleView' $(FRONTEND)/src/App.tsx; \
	grep -q 'overflow-y-auto' $(FRONTEND)/src/App.tsx; \
	printf '%s\n' '[smoke] running frontend unit tests'; \
	cd $(FRONTEND) && node --test tests/*.test.mjs >/dev/null; \
	printf '%s\n' '[smoke] passed'

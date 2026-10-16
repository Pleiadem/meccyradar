.PHONY: test lint build smoke

ROOT := $(CURDIR)
FRONTEND := $(ROOT)/frontend
WAILS ?= $(shell command -v wails 2>/dev/null || printf '%s' /Users/qiuqiu/go/bin/wails)

test:
	go test ./...

lint:
	go vet ./...
	cd $(FRONTEND) && npx tsc --noEmit

build:
	cd $(FRONTEND) && npm run build
	$(WAILS) build -clean

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
	grep -q 'runtime?.Quit' $(FRONTEND)/src/App.tsx; \
	grep -q '授权状态' $(FRONTEND)/src/App.tsx; \
	grep -q '雷达密码' $(FRONTEND)/src/App.tsx; \
	grep -q 'function ConsoleView' $(FRONTEND)/src/App.tsx; \
	grep -q 'overflow-y-auto' $(FRONTEND)/src/App.tsx; \
	printf '%s\n' '[smoke] passed'

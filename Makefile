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
	grep -q 'w-\[52%\]' $(FRONTEND)/src/App.tsx; \
	grep -q 'd8j0ntlcm91z4.cloudfront.net' $(FRONTEND)/src/App.tsx; \
	grep -q 'function StepItem' $(FRONTEND)/src/App.tsx; \
	grep -q 'function SocialButton' $(FRONTEND)/src/App.tsx; \
	grep -q 'function InputGroup' $(FRONTEND)/src/App.tsx; \
	printf '%s\n' '[smoke] passed'

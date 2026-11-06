# Cross-builds the release matrix into dist/ with SHA256SUMS. The release workflow runs this same
# target, so a local run reproduces what a tag publishes.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64
DIST      := dist

.PHONY: release-local
release-local:
	mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		[ "$$os" = windows ] && ext=.exe; \
		echo "build $$os/$$arch" >&2; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags="-s -w" -o $(DIST)/hello-$$os-$$arch$$ext . ; \
	done
	cd $(DIST) && shasum -a 256 hello-* > SHA256SUMS

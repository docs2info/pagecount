# Cross-builds the release matrix into dist/ with checksums.txt, using the same names and flags as
# .github/workflows/release.yml, so a local run reproduces what a tag publishes.
OSES   := linux darwin windows
ARCHES := amd64 arm64
DIST   := dist
# sha256sum is GNU coreutils; macOS ships shasum instead.
SHA256 := $(if $(shell command -v sha256sum 2>/dev/null),sha256sum,shasum -a 256)

.PHONY: release-local
release-local:
	mkdir -p $(DIST)
	@set -e; for os in $(OSES); do for arch in $(ARCHES); do \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		echo "build $$os/$$arch" >&2; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags="-s -w" -o $(DIST)/pagecount_$${os}_$${arch}$$ext . ; \
	done; done
	cd $(DIST) && $(SHA256) pagecount_* > checksums.txt

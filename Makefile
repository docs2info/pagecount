# Cross-builds the release matrix into dist/ as archives with checksums.txt, using the same names and
# flags as .github/workflows/release.yml, so a local run reproduces what a tag publishes. Each archive
# holds pagecount[.exe] and LICENSE; tar keeps the execute bit a bare download loses.
OSES   := linux darwin windows
ARCHES := amd64 arm64
DIST   := dist
STAGE  := $(DIST)/stage
# sha256sum is GNU coreutils; macOS ships shasum instead.
SHA256 := $(if $(shell command -v sha256sum 2>/dev/null),sha256sum,shasum -a 256)

.PHONY: release-local
release-local:
	mkdir -p $(STAGE)
	rm -f $(DIST)/pagecount_* $(DIST)/checksums.txt
	@set -e; for os in $(OSES); do for arch in $(ARCHES); do \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		name=pagecount_$${os}_$${arch}; \
		echo "build $$os/$$arch" >&2; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags="-s -w" -o $(STAGE)/pagecount$$ext . ; \
		cp LICENSE $(STAGE)/LICENSE; \
		if [ "$$os" = windows ]; then \
			(cd $(STAGE) && zip -qX ../$$name.zip pagecount.exe LICENSE); \
		else \
			chmod 755 $(STAGE)/pagecount; \
			COPYFILE_DISABLE=1 tar -czf $(DIST)/$$name.tar.gz -C $(STAGE) pagecount LICENSE; \
		fi; \
		rm -f $(STAGE)/pagecount$$ext $(STAGE)/LICENSE; \
	done; done
	rmdir $(STAGE)
	cd $(DIST) && $(SHA256) pagecount_* > checksums.txt

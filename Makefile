LDFLAGS := -s -w
export CGO_ENABLED := 0

.PHONY: build test vet dist clean

build:
	@mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt needed"; exit 1)

# Static binaries for Linux GPU hosts: dist/linux-<arch>/<cmd>
dist:
	@for arch in amd64 arm64; do \
		mkdir -p dist/linux-$$arch; \
		GOOS=linux GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/linux-$$arch/ ./cmd/... || exit 1; \
	done
	@ls -l dist/*/

clean:
	rm -rf bin dist

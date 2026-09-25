# Docker managed plugins are single-arch: build/push one tag per architecture
# (e.g. TAG=0.1.0-arm64 PLATFORM=linux/arm64) if you run mixed managers. The
# Dockerfile cross-compiles, so any PLATFORM builds natively from any host.
PLUGIN   ?= ghcr.io/andipunz/op-sa-secret-driver
VERSION  ?= 0.1.0
TAG      ?= $(VERSION)
PLATFORM ?=
BUILD    := build

.PHONY: test rootfs plugin enable push clean

test:
	go vet ./...
	go test ./...

rootfs:
	docker build $(if $(PLATFORM),--platform $(PLATFORM)) --build-arg VERSION=$(VERSION) -t $(PLUGIN):rootfs .
	rm -rf $(BUILD) && mkdir -p $(BUILD)/rootfs
	id=$$(docker create $(PLUGIN):rootfs) && \
	  docker export $$id | tar -x -C $(BUILD)/rootfs && \
	  docker rm -vf $$id >/dev/null
	cp config.json $(BUILD)/config.json

plugin: rootfs
	-docker plugin rm -f $(PLUGIN):$(TAG)
	docker plugin create $(PLUGIN):$(TAG) $(BUILD)

# Local convenience: make enable TOKEN=ops_...
enable:
	docker plugin set $(PLUGIN):$(TAG) OP_SERVICE_ACCOUNT_TOKEN=$(TOKEN)
	docker plugin enable $(PLUGIN):$(TAG)

push:
	docker plugin push $(PLUGIN):$(TAG)

clean:
	-docker plugin rm -f $(PLUGIN):$(TAG)
	rm -rf $(BUILD)

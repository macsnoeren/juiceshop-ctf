# Juice Shop CTF — the four things one does with this repository.
#
#   make test     vet, gofmt and the unit tests with the race detector (no Docker)
#   make e2e      the whole CTF in Docker, end to end (tests/e2e.sh, a few minutes)
#   make images   build the images, versioned from git
#   make run      start the test setup (deploy/compose/start.sh); stop with make stop
#
# VERSION comes from `git describe`, as in jmnl-external-servers, so an image
# built from a commit after the tag v1.0.0 calls itself v1.0.0-<n>-g<hash>
# (and -dirty with uncommitted changes). The binaries print it with -version
# and in their first log line; ctf-node sends it in every report, so admin
# shows which VM runs which build.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
export VERSION

.PHONY: test e2e images run stop version

test:
	bash scripts/test.sh

e2e:
	bash tests/e2e.sh

images:
	docker build -f deploy/docker/Dockerfile.node    --build-arg VERSION=$(VERSION) -t juiceshop-ctf:$(VERSION) -t juiceshop-ctf:latest .
	docker build -f deploy/docker/Dockerfile.central --build-arg VERSION=$(VERSION) -t juiceshop-ctf-central:$(VERSION) -t juiceshop-ctf-central:latest .

run:
	deploy/compose/start.sh

stop:
	deploy/compose/stop.sh

version:
	@echo $(VERSION)

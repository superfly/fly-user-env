.PHONY: test

# Default test arguments if none provided
TEST_ARGS ?= ./...

test:
	docker build -t state-manager-test -f Dockerfile.test .
	docker run --rm \
		--privileged \
		-v $(PWD):/app \
		-e FLY_TIGRIS_BUCKET \
		-e FLY_TIGRIS_ENDPOINT_URL \
		-e FLY_TIGRIS_ACCESS_KEY \
		-e FLY_TIGRIS_SECRET_ACCESS_KEY \
		-e LITESTREAM_LOG_LEVEL=error \
		state-manager-test go test -v $(TEST_ARGS) 
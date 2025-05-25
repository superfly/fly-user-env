.PHONY: test

test:
	docker build -t state-manager-test -f Dockerfile.test .
	docker run --rm \
		--privileged \
		-v $(PWD):/app \
		-e FLY_TIGRIS_BUCKET \
		-e FLY_TIGRIS_ENDPOINT_URL \
		-e FLY_TIGRIS_ACCESS_KEY \
		-e FLY_TIGRIS_SECRET_ACCESS_KEY \
		state-manager-test 
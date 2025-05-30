#!/bin/bash

# Check for required environment variables
required_vars=(
    "FLY_TIGRIS_ACCESS_KEY"
    "FLY_TIGRIS_SECRET_ACCESS_KEY"
    "FLY_TIGRIS_BUCKET"
    "CONTROLLER_TOKEN"
    "FLY_TIGRIS_ENDPOINT_URL"
    "FLY_TIGRIS_REGION"
)

for var in "${required_vars[@]}"; do
    if [ -z "${!var}" ]; then
        echo "Error: $var is required but not set"
        exit 1
    fi
done

# Build the Docker image
docker build -t fly-user-env .

# Stop and remove existing container if it exists
docker stop fly-sprite-env 2>/dev/null || true
docker rm fly-sprite-env 2>/dev/null || true

# Run the container with the provided arguments
docker run -it --rm \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    -p 8080:8080 \
    fly-user-env "$@"

# Wait for the container to start
sleep 2

# Configure Tigris credentials via admin API
curl -X POST \
    -H "Host: fly-app-controller" \
    -H "Authorization: Bearer $CONTROLLER_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{
        \"bucket\": \"$FLY_TIGRIS_BUCKET\",
        \"endpoint\": \"$FLY_TIGRIS_ENDPOINT_URL\",
        \"access_key\": \"$FLY_TIGRIS_ACCESS_KEY\",
        \"secret_key\": \"$FLY_TIGRIS_SECRET_ACCESS_KEY\",
        \"region\": \"$FLY_TIGRIS_REGION\"
    }" \
    http://localhost:8080/

echo "Container started and configured. Use 'docker logs fly-sprite-env' to view logs." 
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

# Build the container if it doesn't exist
docker build -t supervisor .

# Stop and remove existing container if it exists
docker stop fly-sprite-env 2>/dev/null || true
docker rm fly-sprite-env 2>/dev/null || true

# Run the container in the background
docker run -d --name fly-sprite-env --privileged \
    -p 8080:8080 \
    -e CONTROLLER_TOKEN="$CONTROLLER_TOKEN" \
    supervisor "$@"

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
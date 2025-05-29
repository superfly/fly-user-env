#!/bin/sh

DOCKER_REPO="flyio/user-env"
PLATFORM="linux/amd64"
VERSION=${1:-"latest"}
TAG=$VERSION

# Validate version format if not latest
if [ "$VERSION" != "latest" ]; then
    if ! echo "$VERSION" | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' > /dev/null; then
        echo "Error: Invalid version format. Use semantic versioning (e.g., v1.0.0)"
        exit 1
    fi
fi

# Set build arguments
BUILD_TIME=$(date -u +'%Y-%m-%d_%H:%M:%S')
GIT_COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "unknown")

echo "Building Docker image for $PLATFORM with version $VERSION..."
docker build \
    --platform $PLATFORM \
    --build-arg VERSION="$VERSION" \
    --build-arg BUILD_TIME="$BUILD_TIME" \
    --build-arg GIT_COMMIT="$GIT_COMMIT" \
    -t $DOCKER_REPO:$TAG .

if [ $? -eq 0 ]; then
    echo "Build successful. Pushing to Docker Hub..."
    docker push $DOCKER_REPO:$TAG
    
    if [ $? -eq 0 ]; then
        echo "Successfully pushed $DOCKER_REPO:$TAG"
    else
        echo "Failed to push image"
        exit 1
    fi
else
    echo "Build failed"
    exit 1
fi

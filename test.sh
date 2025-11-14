#!/bin/bash

set -e

# Container name
POSTGRES_CONTAINER="postgresql_test"

# Define color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

cleanup() {
    echo "Cleaning up..."
    docker stop $POSTGRES_CONTAINER > /dev/null
    docker rm $POSTGRES_CONTAINER > /dev/null
}

trap cleanup EXIT

echo "Starting PostgreSQL container..."
docker run -d \
    --name $POSTGRES_CONTAINER \
    -e POSTGRES_PASSWORD=password \
    -e POSTGRES_DB=better-logs_test \
    -p 5433:5432 \
    postgres:latest > /dev/null

echo "Waiting for services to be ready..."
until docker exec $POSTGRES_CONTAINER pg_isready -U postgres > /dev/null 2>&1; do
    sleep 3
done

echo "Running tests..."
pwd
go test ./... -v -p 1

# Capture the test result
TEST_RESULT=$?

# Check test result and exit accordingly
if [ $TEST_RESULT -eq 0 ]; then
    echo -e "${GREEN}Tests passed!${NC}"
    exit 0
else
    echo -e "${RED}Tests failed!${NC}"
    exit 1
fi

#! /bin/bash
TAG=v1.1.2

set -eou pipefail

# # pull all tags
# git fetch --tags || true

# # check if git tags exist locally, if not, create and push, if yes - echo and skip pushing tag
# if git rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null; then
#   echo "Tag $TAG already exists!"
# else
#   git tag "$TAG"
#   git push origin "$TAG"

#   git tag "pkg/apis/${TAG}"
#   git push origin "pkg/apis/${TAG}"
# fi




read -p "Have you logged in to aws and ecr? ([y]/n): " confirm
if [ "$confirm" == "n" ]; then    
    echo "Logging in to aws and ecr"
    aws sso login
    aws ecr-public get-login-password --region us-east-1 | docker login --username AWS --password-stdin public.ecr.aws
fi

echo "Building and deploying $TAG"
make build
# echo "Running tests"
# make test > /dev/null 2>&1
# if [ $? -ne 0 ]; then
#     echo "Tests failed"    
#     exit 1
# fi
make image TAG=public.ecr.aws/k6t4m3l7/ib-kubernetes:$TAG
# ask to confirm
read -p "Are you sure you want to push and deploy $TAG? (y/[n]): " confirm
if [ "$confirm" != "y" ]; then
    echo "Deployment cancelled"
    exit 1
fi
make docker-push TAG=public.ecr.aws/k6t4m3l7/ib-kubernetes:$TAG

echo "To Deploy, install/upgrade via helm!"
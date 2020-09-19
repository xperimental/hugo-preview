#!/usr/bin/env bash

set -e -u -o pipefail

TAG=$(git rev-parse --short HEAD)
readonly TAG

docker build -t "xperimental/hugo-preview:$TAG" .

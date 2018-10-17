#!/usr/bin/env bash
set -ex

go vet ./...

# TODO(joelanford): add license headers to files
#./hack/check_license.sh

# TODO(joelanford): fix case of error messages
#./hack/check_error_case.sh

# Make sure repo is in clean state
git diff --exit-code

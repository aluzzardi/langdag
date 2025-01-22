#!/bin/bash

go run . \
	"Whenever a new pull request is open, scan it for leaked secrets. If you find any, add a comment to the PR with a report." \
	../../modules/github ../../modules/trufflehog

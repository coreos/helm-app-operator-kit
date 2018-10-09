#!/bin/sh

unformatted=$(gofmt -l $(go list -f "{{.Dir}}" ./...))
[ -z "$unformatted" ] && exit 0

echo >&2 "Go files must be formatted with gofmt. Please run:"
for fn in $unformatted; do
  echo >&2 "  gofmt -w $fn"
done

exit 1

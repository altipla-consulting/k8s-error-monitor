
FILES = $(shell find . -type f -name '*.go')

gofmt:
	@gofmt -s -w $(FILES)
	@gofmt -r '&a{} -> new(a)' -w $(FILES)

update-deps:
	go get -u all
	go mod download
	go mod tidy

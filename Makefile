
FILES = $(shell find . -type f -name '*.go')

gofmt:
	@gofmt -s -w $(FILES)
	@gofmt -r '&a{} -> new(a)' -w $(FILES)

update-deps:
	go get -u all
	go mod download
	go mod tidy

deploy:
ifndef tag
	$(error tag is not set)
endif
	docker-compose build
	docker tag altipla/k8s-error-monitor:latest altipla/k8s-error-monitor:$(tag)
	docker push altipla/k8s-error-monitor:$(tag)
	docker push altipla/k8s-error-monitor:latest
	git tag $(tag)
	git push origin --tags

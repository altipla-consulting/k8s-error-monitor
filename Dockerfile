
FROM golang:latest as builder

COPY . /workspace

WORKDIR /workspace
RUN go build -o /workspace/k8s-error-monitor .

FROM launcher.gcr.io/google/debian9:latest

RUN apt-get update && \
    apt-get install -y ca-certificates

WORKDIR /opt/ac
COPY --from=builder /workspace/k8s-error-monitor .

CMD ["/opt/ac/k8s-error-monitor"]

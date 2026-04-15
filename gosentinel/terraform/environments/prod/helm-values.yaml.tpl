pipeline:
  image:
    repository: "${split(":", pipeline_image)[0]}"
    tag: "${split(":", pipeline_image)[1]}"
  replicaCount: 2

api:
  image:
    repository: "${split(":", api_image)[0]}"
    tag: "${split(":", api_image)[1]}"
  replicaCount: 3

ui:
  image:
    repository: "${split(":", ui_image)[0]}"
    tag: "${split(":", ui_image)[1]}"
  replicaCount: 2

backends:
  otlpEndpoint: "otel-collector.gosentinel.svc.cluster.local:4317"
  victoriaMetricsEndpoint: "http://victoriametrics.gosentinel.svc.cluster.local:8428"
  jaegerEndpoint: "jaeger-query.gosentinel.svc.cluster.local:16685"
  lokiEndpoint: "http://loki.gosentinel.svc.cluster.local:3100"
  pyroscopeEndpoint: "http://pyroscope.gosentinel.svc.cluster.local:4040"

aws:
  region: "${aws_region}"
  dbSecretArn: "${db_secret_arn}"

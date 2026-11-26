//go:build !no_apm

package telemetry

import (
	"context"
	"net/http"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestServiceNameFromAgentProxyDomain(t *testing.T) {
	t.Run("registers AGENT_PROXY_DOMAIN when explicit service name is unset", func(t *testing.T) {
		t.Setenv("DD_SERVICE", "")
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("AGENT_PROXY_DOMAIN", "jp-xconnect-01.svc.plus")
		if got := DDTraceServiceName(Options{}); got != "jp-xconnect-01.svc.plus" {
			t.Fatalf("DDTraceServiceName() = %q, want %q", got, "jp-xconnect-01.svc.plus")
		}
		if got := OTelServiceName(Options{}); got != "jp-xconnect-01.svc.plus" {
			t.Fatalf("OTelServiceName() = %q, want %q", got, "jp-xconnect-01.svc.plus")
		}
	})

	t.Run("falls back to Options.ProxyDomain and default service name", func(t *testing.T) {
		t.Setenv("DD_SERVICE", "")
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("AGENT_PROXY_DOMAIN", "")
		if got := DDTraceServiceName(Options{ProxyDomain: "us-xhttp.svc.plus"}); got != "us-xhttp.svc.plus" {
			t.Fatalf("DDTraceServiceName() = %q, want %q", got, "us-xhttp.svc.plus")
		}
		if got := DDTraceServiceName(Options{}); got != defaultServiceName {
			t.Fatalf("DDTraceServiceName() = %q, want %q", got, defaultServiceName)
		}
	})
}

func TestResolveTracingProviderWithOptions(t *testing.T) {
	t.Run("defaults to none when DD_TRACE_ENABLED is unset or false", func(t *testing.T) {
		t.Setenv("TRACING_PROVIDER", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
		t.Setenv("DD_AGENT_HOST", "127.0.0.1")
		t.Setenv("DD_TRACE_ENABLED", "")
		if got := ResolveTracingProviderWithOptions(Options{}); got != ProviderNone {
			t.Fatalf("unset DD_TRACE_ENABLED: got %q, want %q", got, ProviderNone)
		}
		t.Setenv("DD_TRACE_ENABLED", "false")
		if got := ResolveTracingProviderWithOptions(Options{DDTraceEnabled: boolPtr(true), DDAgentHost: "127.0.0.1"}); got != ProviderNone {
			t.Fatalf("DD_TRACE_ENABLED=false should override config true: got %q", got)
		}
	})

	t.Run("config file enables ddtrace when env does not disable it", func(t *testing.T) {
		t.Setenv("TRACING_PROVIDER", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("DD_AGENT_HOST", "")
		t.Setenv("DD_TRACE_ENABLED", "")
		opts := Options{DDTraceEnabled: boolPtr(true), DDAgentHost: "127.0.0.1"}
		if got := ResolveTracingProviderWithOptions(opts); got != ProviderDDTrace {
			t.Fatalf("got %q, want %q", got, ProviderDDTrace)
		}
	})

	t.Run("prefers OTel over DDTrace unless OTEL_SDK_DISABLED=true", func(t *testing.T) {
		t.Setenv("TRACING_PROVIDER", "")
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:4318")
		t.Setenv("OTEL_SDK_DISABLED", "")
		t.Setenv("DD_AGENT_HOST", "127.0.0.1")
		t.Setenv("DD_TRACE_ENABLED", "true")
		if got := ResolveTracingProviderWithOptions(Options{}); got != ProviderOTel {
			t.Fatalf("got %q, want %q", got, ProviderOTel)
		}

		t.Setenv("OTEL_SDK_DISABLED", "true")
		if got := ResolveTracingProviderWithOptions(Options{}); got != ProviderDDTrace {
			t.Fatalf("with OTEL_SDK_DISABLED=true got %q, want %q", got, ProviderDDTrace)
		}
	})
}

func TestDDTraceAgentAddr(t *testing.T) {
	t.Setenv("DD_AGENT_HOST", "datakit-service.observability-prod.svc")
	t.Setenv("DD_TRACE_AGENT_PORT", "")
	if got := DDTraceAgentAddr(); got != "datakit-service.observability-prod.svc:9529" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("DD_TRACE_AGENT_PORT", "19529")
	if got := DDTraceAgentAddr(); got != "datakit-service.observability-prod.svc:19529" {
		t.Fatalf("got %q", got)
	}
}

func TestWrapHTTPClientAndSpanLifecycle(t *testing.T) {
	setActiveState(ProviderNone, defaultServiceName)
	base := &http.Client{}
	if got := WrapHTTPClient(base); got != base {
		t.Fatalf("ProviderNone should return client unchanged")
	}

	setActiveState(ProviderOTel, "edge.example.test")
	wrappedOTel := WrapHTTPClient(&http.Client{})
	if wrappedOTel.Transport == nil {
		t.Fatalf("ProviderOTel expected otelhttp transport")
	}

	setActiveState(ProviderDDTrace, "edge.example.test")
	wrappedDD := WrapHTTPClient(&http.Client{})
	if wrappedDD.Transport == nil {
		t.Fatalf("ProviderDDTrace expected ddtrace transport")
	}

	ctx, sp := StartSpan(context.Background(), "test.op")
	if ctx == nil || sp == nil {
		t.Fatalf("expected non-nil ctx and span")
	}
	sp.SetString("k", "v")
	sp.SetInt("i", 1)
	sp.SetInt64("i64", 2)
	sp.AddEvent("ev", StringKV("a", "b"))
	sp.End()
	setActiveState(ProviderNone, defaultServiceName)
}

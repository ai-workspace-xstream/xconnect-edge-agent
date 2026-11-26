//go:build no_apm

package telemetry

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultServiceName = "xconnect-edge-agent"
	defaultDDPort      = "9529"

	ProviderNone    = "none"
	ProviderOTel    = "otel"
	ProviderDDTrace = "ddtrace"
)

// Options mirrors the telemetry configuration struct when APM SDKs are compiled out.
type Options struct {
	Provider            string
	ServiceName         string
	Env                 string
	ProxyDomain         string
	OTelEnabled         *bool
	OTelEndpoint        string
	OTelTracesEndpoint  string
	OTelMetricsEndpoint string
	DDTraceEnabled      *bool
	DDAgentHost         string
	DDAgentPort         string
}

// KeyValue represents a generic telemetry span attribute.
type KeyValue struct {
	Key   string
	Value string
}

// StringKV builds a string KeyValue pair.
func StringKV(k, v string) KeyValue {
	return KeyValue{Key: k, Value: v}
}

// Span is a no-op stub when compiled with -tags no_apm.
type Span struct{}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// OTelServiceName resolves the service name from env/config/AGENT_PROXY_DOMAIN.
func OTelServiceName(opts Options) string {
	return firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), opts.ServiceName, os.Getenv("AGENT_PROXY_DOMAIN"), opts.ProxyDomain, defaultServiceName)
}

// DDTraceServiceName resolves the service name from env/config/AGENT_PROXY_DOMAIN.
func DDTraceServiceName(opts Options) string {
	return firstNonEmpty(os.Getenv("DD_SERVICE"), os.Getenv("OTEL_SERVICE_NAME"), opts.ServiceName, os.Getenv("AGENT_PROXY_DOMAIN"), opts.ProxyDomain, defaultServiceName)
}

// DDTraceAgentAddr builds host:port for DataKit DDTrace ingestion.
func DDTraceAgentAddr() string {
	return DDTraceAgentAddrWithOptions(Options{})
}

// DDTraceAgentAddrWithOptions resolves DataKit DDTrace host:port.
func DDTraceAgentAddrWithOptions(opts Options) string {
	host := firstNonEmpty(os.Getenv("DD_AGENT_HOST"), opts.DDAgentHost, "127.0.0.1")
	port := firstNonEmpty(os.Getenv("DD_TRACE_AGENT_PORT"), opts.DDAgentPort, defaultDDPort)
	return net.JoinHostPort(host, port)
}

// ResolveTracingProvider always returns ProviderNone when compiled with -tags no_apm.
func ResolveTracingProvider() string {
	return ProviderNone
}

// ResolveTracingProviderWithOptions always returns ProviderNone when compiled with -tags no_apm.
func ResolveTracingProviderWithOptions(Options) string {
	return ProviderNone
}

// Init is a no-op when compiled with -tags no_apm.
func Init(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

// InitWithOptions is a no-op when compiled with -tags no_apm.
func InitWithOptions(context.Context, string, Options) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

// RecordSync is a no-op when compiled with -tags no_apm.
func RecordSync(context.Context, time.Duration, error) {}

// RecordRestart is a no-op when compiled with -tags no_apm.
func RecordRestart(context.Context, time.Duration, error) {}

// StartSpan is a no-op when compiled with -tags no_apm.
func StartSpan(ctx context.Context, _ string) (context.Context, *Span) {
	return ctx, &Span{}
}

func (*Span) SetString(string, string)       {}
func (*Span) SetInt(string, int)             {}
func (*Span) SetInt64(string, int64)         {}
func (*Span) AddEvent(string, ...KeyValue)   {}
func (*Span) RecordError(error, string)      {}
func (*Span) End()                           {}

// WrapHTTPClient returns client unchanged when compiled with -tags no_apm.
func WrapHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{}
	}
	return client
}

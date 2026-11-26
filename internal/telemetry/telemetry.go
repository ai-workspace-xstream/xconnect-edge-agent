//go:build !no_apm

// Package telemetry configures optional node-side OpenTelemetry or DDTrace APM.
// Both backends remain disabled unless explicitly enabled by configuration or env.
package telemetry

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.37.0"
)

const (
	defaultServiceName = "xconnect-edge-agent"
	defaultDDPort      = "9529"
	shutdownTimeout    = 2 * time.Second

	ProviderNone    = "none"
	ProviderOTel    = "otel"
	ProviderDDTrace = "ddtrace"
)

// Options holds telemetry configuration bridged from account-agent.yaml and agent metadata.
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

var (
	stateMu        sync.RWMutex
	activeProvider = ProviderNone
	activeService  = defaultServiceName

	syncDuration    metric.Float64Histogram
	syncErrors      metric.Int64Counter
	restartCount    metric.Int64Counter
	restartDuration metric.Float64Histogram
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resolveProxyDomain(opts Options) string {
	return firstNonEmpty(os.Getenv("AGENT_PROXY_DOMAIN"), opts.ProxyDomain)
}

// OTelServiceName resolves the OTel service name, prioritizing explicit env/config
// and otherwise registering by AGENT_PROXY_DOMAIN.
func OTelServiceName(opts Options) string {
	return firstNonEmpty(
		os.Getenv("OTEL_SERVICE_NAME"),
		opts.ServiceName,
		os.Getenv("AGENT_PROXY_DOMAIN"),
		opts.ProxyDomain,
		defaultServiceName,
	)
}

// DDTraceServiceName resolves the DDTrace service name, prioritizing explicit env/config
// and otherwise registering by AGENT_PROXY_DOMAIN.
func DDTraceServiceName(opts Options) string {
	return firstNonEmpty(
		os.Getenv("DD_SERVICE"),
		os.Getenv("OTEL_SERVICE_NAME"),
		opts.ServiceName,
		os.Getenv("AGENT_PROXY_DOMAIN"),
		opts.ProxyDomain,
		defaultServiceName,
	)
}

// DDTraceAgentAddr builds host:port for DataKit DDTrace ingestion (default port 9529).
func DDTraceAgentAddr() string {
	return DDTraceAgentAddrWithOptions(Options{})
}

// DDTraceAgentAddrWithOptions resolves DataKit DDTrace host:port from env or config options.
func DDTraceAgentAddrWithOptions(opts Options) string {
	host := firstNonEmpty(os.Getenv("DD_AGENT_HOST"), opts.DDAgentHost, "127.0.0.1")
	port := firstNonEmpty(os.Getenv("DD_TRACE_AGENT_PORT"), opts.DDAgentPort, defaultDDPort)
	return net.JoinHostPort(host, port)
}

func isOTelEnabled(opts Options) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return false
	}
	if opts.OTelEnabled != nil && !*opts.OTelEnabled && strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")) == "" {
		// Allow explicit env endpoints only if config didn't disable OTel, unless OTEL_SDK_DISABLED=false.
		return false
	}
	commonEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), opts.OTelEndpoint)
	tracesEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), opts.OTelTracesEndpoint)
	metricsEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"), opts.OTelMetricsEndpoint)
	return commonEndpoint != "" || tracesEndpoint != "" || metricsEndpoint != ""
}

func isDDTraceEnabled(opts Options) bool {
	envFlag := strings.ToLower(strings.TrimSpace(os.Getenv("DD_TRACE_ENABLED")))
	if envFlag == "false" || envFlag == "0" {
		return false
	}
	explicitEnabled := envFlag == "true" || envFlag == "1"
	if !explicitEnabled {
		if envFlag == "" && opts.DDTraceEnabled != nil && *opts.DDTraceEnabled {
			explicitEnabled = true
		} else {
			return false
		}
	}
	host := firstNonEmpty(os.Getenv("DD_TRACE_AGENT_URL"), os.Getenv("DD_AGENT_HOST"), opts.DDAgentHost)
	return host != ""
}

// ResolveTracingProvider returns the active provider based on environment variables.
func ResolveTracingProvider() string {
	return ResolveTracingProviderWithOptions(Options{})
}

// ResolveTracingProviderWithOptions selects OTel or DDTrace with OTel-first mutual exclusion
// and explicit env/config on/off switches (DDTrace defaults to off).
func ResolveTracingProviderWithOptions(opts Options) string {
	override := strings.ToLower(firstNonEmpty(os.Getenv("TRACING_PROVIDER"), opts.Provider))
	switch override {
	case ProviderNone:
		return ProviderNone
	case ProviderOTel:
		if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
			return ProviderNone
		}
		return ProviderOTel
	case ProviderDDTrace:
		if strings.EqualFold(strings.TrimSpace(os.Getenv("DD_TRACE_ENABLED")), "false") {
			return ProviderNone
		}
		if strings.TrimSpace(os.Getenv("DD_TRACE_ENABLED")) == "" && opts.DDTraceEnabled != nil && !*opts.DDTraceEnabled {
			return ProviderNone
		}
		return ProviderDDTrace
	}

	if isOTelEnabled(opts) {
		return ProviderOTel
	}
	if isDDTraceEnabled(opts) {
		return ProviderDDTrace
	}
	return ProviderNone
}

func setActiveState(provider, service string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	activeProvider = provider
	if strings.TrimSpace(service) != "" {
		activeService = service
	} else {
		activeService = defaultServiceName
	}
}

func getActiveState() (string, string) {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return activeProvider, activeService
}

// Init installs telemetry providers using environment configuration.
func Init(ctx context.Context, serviceVersion string) (func(context.Context) error, error) {
	return InitWithOptions(ctx, serviceVersion, Options{})
}

// InitWithOptions installs OTel or DDTrace providers from merged config options and env vars.
func InitWithOptions(ctx context.Context, serviceVersion string, opts Options) (func(context.Context) error, error) {
	provider := ResolveTracingProviderWithOptions(opts)
	switch provider {
	case ProviderDDTrace:
		svc := DDTraceServiceName(opts)
		envName := firstNonEmpty(
			os.Getenv("DD_ENV"),
			os.Getenv("XCONNECT_ENVIRONMENT"),
			os.Getenv("OTEL_ENVIRONMENT"),
			os.Getenv("ENVIRONMENT"),
			opts.Env,
			"unknown",
		)
		startOpts := []tracer.StartOption{
			tracer.WithService(svc),
			tracer.WithEnv(envName),
		}
		if strings.TrimSpace(os.Getenv("DD_TRACE_AGENT_URL")) == "" {
			startOpts = append(startOpts, tracer.WithAgentAddr(DDTraceAgentAddrWithOptions(opts)))
		}
		if version := firstNonEmpty(os.Getenv("DD_VERSION"), serviceVersion); version != "" {
			startOpts = append(startOpts, tracer.WithServiceVersion(version))
		}
		if nodeID := strings.TrimSpace(os.Getenv("XCONNECT_NODE_ID")); nodeID != "" {
			startOpts = append(startOpts, tracer.WithGlobalTag("xconnect.node.id", nodeID))
		}
		if proxyDomain := resolveProxyDomain(opts); proxyDomain != "" {
			startOpts = append(startOpts, tracer.WithGlobalTag("proxy_domain", proxyDomain))
		}
		if err := tracer.Start(startOpts...); err != nil {
			setActiveState(ProviderNone, defaultServiceName)
			return func(context.Context) error { return nil }, err
		}
		setActiveState(ProviderDDTrace, svc)
		return func(context.Context) error {
			tracer.Stop()
			setActiveState(ProviderNone, defaultServiceName)
			return nil
		}, nil

	case ProviderOTel:
		return initOTel(ctx, serviceVersion, opts)

	default:
		setActiveState(ProviderNone, defaultServiceName)
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		))
		return func(context.Context) error { return nil }, nil
	}
}

func initOTel(ctx context.Context, serviceVersion string, opts Options) (func(context.Context) error, error) {
	svc := OTelServiceName(opts)
	attributes := []attribute.KeyValue{semconv.ServiceName(svc)}
	if serviceVersion != "" {
		attributes = append(attributes, semconv.ServiceVersion(serviceVersion))
	}
	if nodeID := strings.TrimSpace(os.Getenv("XCONNECT_NODE_ID")); nodeID != "" {
		attributes = append(attributes, attribute.String("xconnect.node.id", nodeID))
	}
	if proxyDomain := resolveProxyDomain(opts); proxyDomain != "" {
		attributes = append(attributes, attribute.String("xconnect.proxy.domain", proxyDomain))
	}
	if environment := firstNonEmpty(os.Getenv("XCONNECT_ENVIRONMENT"), os.Getenv("OTEL_ENVIRONMENT"), opts.Env); environment != "" {
		attributes = append(attributes, attribute.String("deployment.environment.name", environment))
	}
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		return nil, err
	}

	commonEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), opts.OTelEndpoint)
	tracesEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), opts.OTelTracesEndpoint)
	metricsEndpoint := firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"), opts.OTelMetricsEndpoint)

	var traceProvider *sdktrace.TracerProvider
	if tracesEndpoint != "" || commonEndpoint != "" {
		var traceOpts []otlptracehttp.Option
		if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
			if target := firstNonEmpty(tracesEndpoint, commonEndpoint); target != "" {
				traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(target))
			}
		}
		exporter, exportErr := otlptracehttp.New(ctx, traceOpts...)
		if exportErr != nil {
			return nil, exportErr
		}
		traceProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(exporter,
				sdktrace.WithMaxQueueSize(2048),
				sdktrace.WithBatchTimeout(5*time.Second),
				sdktrace.WithExportTimeout(3*time.Second),
			),
		)
		otel.SetTracerProvider(traceProvider)
	}

	var meterProvider *sdkmetric.MeterProvider
	if metricsEndpoint != "" || commonEndpoint != "" {
		var metricOpts []otlpmetrichttp.Option
		if os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
			if target := firstNonEmpty(metricsEndpoint, commonEndpoint); target != "" {
				metricOpts = append(metricOpts, otlpmetrichttp.WithEndpointURL(target))
			}
		}
		exporter, exportErr := otlpmetrichttp.New(ctx, metricOpts...)
		if exportErr != nil {
			if traceProvider != nil {
				_ = traceProvider.Shutdown(ctx)
			}
			return nil, exportErr
		}
		reader := sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(30*time.Second),
			sdkmetric.WithTimeout(3*time.Second),
		)
		meterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
		otel.SetMeterProvider(meterProvider)
		meter := meterProvider.Meter("github.com/ai-workspace-xstream/xconnect-edge-agent/internal/telemetry")
		syncDuration, err = meter.Float64Histogram("xconnect.agent.config_sync.duration", metric.WithUnit("s"))
		if err != nil {
			return nil, err
		}
		syncErrors, err = meter.Int64Counter("xconnect.agent.config_sync.errors")
		if err != nil {
			return nil, err
		}
		restartCount, err = meter.Int64Counter("xconnect.agent.xray.restart")
		if err != nil {
			return nil, err
		}
		restartDuration, err = meter.Float64Histogram("xconnect.agent.xray.restart.duration", metric.WithUnit("s"))
		if err != nil {
			return nil, err
		}
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	setActiveState(ProviderOTel, svc)

	return func(shutdownCtx context.Context) error {
		if shutdownCtx == nil {
			var cancel context.CancelFunc
			shutdownCtx, cancel = context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
		}
		var shutdownErr error
		if meterProvider != nil {
			shutdownErr = meterProvider.Shutdown(shutdownCtx)
		}
		if traceProvider != nil {
			shutdownErr = errors.Join(shutdownErr, traceProvider.Shutdown(shutdownCtx))
		}
		setActiveState(ProviderNone, defaultServiceName)
		return shutdownErr
	}, nil
}

// RecordSync emits aggregate measurements without user or connection labels.
func RecordSync(ctx context.Context, duration time.Duration, err error) {
	if syncDuration == nil {
		return
	}
	syncDuration.Record(ctx, duration.Seconds())
	if err != nil && syncErrors != nil {
		syncErrors.Add(ctx, 1)
	}
}

// RecordRestart records restart attempts without command or user attributes.
func RecordRestart(ctx context.Context, duration time.Duration, err error) {
	if restartCount == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	restartCount.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	if restartDuration != nil {
		restartDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

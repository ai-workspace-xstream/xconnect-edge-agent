// Package telemetry configures the optional node-side OpenTelemetry SDK.
// Export is disabled unless an OTLP endpoint is explicitly configured.
package telemetry

import (
	"context"
	"errors"
	"os"
	"time"

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
	shutdownTimeout    = 2 * time.Second
)

var (
	syncDuration    metric.Float64Histogram
	syncErrors      metric.Int64Counter
	restartCount    metric.Int64Counter
	restartDuration metric.Float64Histogram
)

// Init installs OTLP/HTTP providers if a standard OTLP endpoint is configured.
// Standard OTEL_EXPORTER_OTLP_HEADERS configuration is supported by the SDK.
func Init(ctx context.Context, serviceVersion string) (func(context.Context) error, error) {
	commonEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	tracesEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	metricsEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if commonEndpoint == "" && tracesEndpoint == "" && metricsEndpoint == "" {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		))
		return func(context.Context) error { return nil }, nil
	}

	attributes := []attribute.KeyValue{semconv.ServiceName(defaultServiceName)}
	if serviceVersion != "" {
		attributes = append(attributes, semconv.ServiceVersion(serviceVersion))
	}
	if nodeID := os.Getenv("XCONNECT_NODE_ID"); nodeID != "" {
		attributes = append(attributes, attribute.String("xconnect.node.id", nodeID))
	}
	if environment := os.Getenv("XCONNECT_ENVIRONMENT"); environment != "" {
		attributes = append(attributes, attribute.String("deployment.environment.name", environment))
	}
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		return nil, err
	}

	var traceProvider *sdktrace.TracerProvider
	if tracesEndpoint != "" || commonEndpoint != "" {
		exporter, exportErr := otlptracehttp.New(ctx)
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
		exporter, exportErr := otlpmetrichttp.New(ctx)
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

//go:build !no_apm

package telemetry

import (
	"context"
	"net/http"

	httptrace "github.com/DataDog/dd-trace-go/contrib/net/http/v2"
	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// KeyValue represents a generic telemetry span attribute.
type KeyValue struct {
	Key   string
	Value string
}

// StringKV builds a string KeyValue pair for span events.
func StringKV(k, v string) KeyValue {
	return KeyValue{Key: k, Value: v}
}

// Span abstracts OTel and DDTrace spans so callers compile cleanly with -tags no_apm.
type Span struct {
	otelSpan trace.Span
	ddSpan   *tracer.Span
}

// StartSpan starts a span on whichever tracing backend (OTel or DDTrace) is active.
func StartSpan(ctx context.Context, operation string) (context.Context, *Span) {
	provider, _ := getActiveState()
	switch provider {
	case ProviderOTel:
		nextCtx, s := otel.Tracer("github.com/ai-workspace-xstream/xconnect-edge-agent").Start(ctx, operation)
		return nextCtx, &Span{otelSpan: s}
	case ProviderDDTrace:
		s, nextCtx := tracer.StartSpanFromContext(ctx, operation)
		return nextCtx, &Span{ddSpan: s}
	default:
		return ctx, &Span{}
	}
}

// SetString sets a string attribute/tag on the span.
func (s *Span) SetString(k, v string) {
	if s == nil {
		return
	}
	if s.otelSpan != nil {
		s.otelSpan.SetAttributes(attribute.String(k, v))
	}
	if s.ddSpan != nil {
		s.ddSpan.SetTag(k, v)
	}
}

// SetInt sets an integer attribute/tag on the span.
func (s *Span) SetInt(k string, v int) {
	if s == nil {
		return
	}
	if s.otelSpan != nil {
		s.otelSpan.SetAttributes(attribute.Int(k, v))
	}
	if s.ddSpan != nil {
		s.ddSpan.SetTag(k, v)
	}
}

// SetInt64 sets an int64 attribute/tag on the span.
func (s *Span) SetInt64(k string, v int64) {
	if s == nil {
		return
	}
	if s.otelSpan != nil {
		s.otelSpan.SetAttributes(attribute.Int64(k, v))
	}
	if s.ddSpan != nil {
		s.ddSpan.SetTag(k, v)
	}
}

// AddEvent records a named event on OTel or attaches event tags on DDTrace.
func (s *Span) AddEvent(name string, attrs ...KeyValue) {
	if s == nil {
		return
	}
	if s.otelSpan != nil {
		kv := make([]attribute.KeyValue, 0, len(attrs))
		for _, a := range attrs {
			kv = append(kv, attribute.String(a.Key, a.Value))
		}
		s.otelSpan.AddEvent(name, trace.WithAttributes(kv...))
	}
	if s.ddSpan != nil {
		s.ddSpan.SetTag("event."+name, true)
		for _, a := range attrs {
			s.ddSpan.SetTag(name+"."+a.Key, a.Value)
		}
	}
}

// RecordError marks the span as failed with error metadata.
func (s *Span) RecordError(err error, msg string) {
	if s == nil || err == nil {
		return
	}
	if s.otelSpan != nil {
		s.otelSpan.RecordError(err)
		s.otelSpan.SetStatus(codes.Error, msg)
	}
	if s.ddSpan != nil {
		s.ddSpan.SetTag("error", true)
		s.ddSpan.SetTag("error.msg", msg)
	}
}

// End finishes the active span.
func (s *Span) End() {
	if s == nil {
		return
	}
	if s.otelSpan != nil {
		s.otelSpan.End()
	}
	if s.ddSpan != nil {
		s.ddSpan.Finish()
	}
}

// WrapHTTPClient instruments an outbound http.Client with OTel or DDTrace according to the active provider.
func WrapHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	provider, svc := getActiveState()
	switch provider {
	case ProviderOTel:
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		cloned := *client
		cloned.Transport = otelhttp.NewTransport(base)
		return &cloned
	case ProviderDDTrace:
		return httptrace.WrapClient(client, httptrace.WithService(svc))
	default:
		return client
	}
}

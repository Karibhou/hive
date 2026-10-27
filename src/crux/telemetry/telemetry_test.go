package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSignalsEnabled(t *testing.T) {
	cases := []struct {
		name            string
		env             map[string]string
		traces, metrics bool
	}{
		{"unset", nil, false, false},
		{"shared endpoint", map[string]string{envEndpoint: "http://collector:4318"}, true, true},
		{"traces only", map[string]string{envTracesEndpoint: "http://c/v1/traces"}, true, false},
		{"metrics only", map[string]string{envMetricsEndpoint: "http://c/v1/metrics"}, false, true},
		{"blank endpoint", map[string]string{envEndpoint: "  "}, false, false},
		{"sdk disabled", map[string]string{envEndpoint: "http://c", envSDKDisabled: "TRUE"}, false, false},
		{"traces exporter none", map[string]string{envEndpoint: "http://c", envTracesExporter: "none"}, false, true},
		{"metrics exporter none", map[string]string{envEndpoint: "http://c", envMetricsExporter: "None"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			traces, metrics := signalsEnabled(envOf(tc.env))
			if traces != tc.traces || metrics != tc.metrics {
				t.Fatalf("signalsEnabled = (%v, %v), want (%v, %v)", traces, metrics, tc.traces, tc.metrics)
			}
		})
	}
}

// With no endpoint configured Init must leave the global providers alone:
// that is the zero-behaviour-change contract for current users.
func TestInitWithoutEndpointKeepsNoopProviders(t *testing.T) {
	tp, mp := otel.GetTracerProvider(), otel.GetMeterProvider()
	shutdown, err := initFromEnv(context.Background(), "v1", envOf(nil))
	if err != nil {
		t.Fatalf("initFromEnv: %v", err)
	}
	shutdown()
	shutdown()
	if otel.GetTracerProvider() != tp || otel.GetMeterProvider() != mp {
		t.Fatal("initFromEnv replaced a global provider without an endpoint")
	}
}

func TestInitRejectsGRPCProtocol(t *testing.T) {
	tp := otel.GetTracerProvider()
	_, err := initFromEnv(context.Background(), "v1", envOf(map[string]string{
		envEndpoint: "http://collector:4317",
		envProtocol: "grpc",
	}))
	if err == nil || !strings.Contains(err.Error(), envProtocol) {
		t.Fatalf("err = %v, want an unsupported-protocol error naming %s", err, envProtocol)
	}
	if otel.GetTracerProvider() != tp {
		t.Fatal("a rejected configuration must not install a provider")
	}
}

func TestNewResourceDefaultsAndEnvOverride(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	res, err := newResource(context.Background(), "1.2.3")
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	if got, _ := res.Set().Value(attribute.Key("service.name")); got.AsString() != defaultServiceName {
		t.Fatalf("service.name = %q, want %q", got.AsString(), defaultServiceName)
	}
	if got, _ := res.Set().Value(attribute.Key("service.version")); got.AsString() != "1.2.3" {
		t.Fatalf("service.version = %q, want 1.2.3", got.AsString())
	}

	t.Setenv("OTEL_SERVICE_NAME", "crux-edge")
	res, err = newResource(context.Background(), "")
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	if got, _ := res.Set().Value(attribute.Key("service.name")); got.AsString() != "crux-edge" {
		t.Fatalf("service.name = %q, want OTEL_SERVICE_NAME to override", got.AsString())
	}
	if _, ok := res.Set().Value(attribute.Key("service.version")); ok {
		t.Fatal("an empty version must not stamp service.version")
	}
}

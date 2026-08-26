//go:build linux && shim_tracing

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package task

import (
	"context"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/containerd/ttrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// envCarrier implements propagation.TextMapCarrier for environment variables,
// adhering to the OpenTelemetry "Environment Variables as Context Propagation Carriers" specification:
// https://opentelemetry.io/docs/specs/otel/context/env-carriers/
//
// This enables the shim to propagate the active trace context (TRACEPARENT, TRACESTATE, BAGGAGE)
// down to spawned processes (runc and OCI lifecycle hooks).
// Ref: PR #14036 (https://github.com/containerd/containerd/pull/14036)
type envCarrier struct{}

var _ propagation.TextMapCarrier = (*envCarrier)(nil)

func normalize(s string) string {
	if s == "" {
		return "_"
	}
	var b []byte
	i := 0
	if s[0] >= '0' && s[0] <= '9' {
		b = make([]byte, utf8.RuneCountInString(s)+1)
		b[0] = '_'
		i = 1
	} else {
		b = make([]byte, utf8.RuneCountInString(s))
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b[i] = byte(r)
		case r >= 'a' && r <= 'z':
			b[i] = byte(r + 'A' - 'a')
		default:
			b[i] = '_'
		}
		i++
	}
	return string(b)
}

func normalized(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func (*envCarrier) Get(key string) string {
	return os.Getenv(normalize(key))
}

func (*envCarrier) Set(key, value string) {
	os.Setenv(normalize(key), value)
}

func (*envCarrier) Keys() []string {
	environ := os.Environ()
	keys := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if normalized(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// UnaryServerInterceptor propagates the trace context of the request to the
// runc invocations made while serving it.
//
// The trace context itself is extracted from the request metadata by the
// otelttrpc interceptor, which pkg/shim always chains first.
//
// runc and the OCI hooks it spawns are separate processes, so the context can
// not be passed over ttrpc. It travels in their environment instead, as
// TRACEPARENT, TRACESTATE and BAGGAGE, see
// https://opentelemetry.io/docs/specs/otel/context/env-carriers/
// Ref: PR #14036 (https://github.com/containerd/containerd/pull/14036)
func (*service) UnaryServerInterceptor() ttrpc.UnaryServerInterceptor {
	return func(ctx context.Context, unmarshal ttrpc.Unmarshaler, info *ttrpc.UnaryServerInfo, method ttrpc.Method) (any, error) {
		carrier := &envCarrier{}
		otel.GetTextMapPropagator().Inject(ctx, carrier)

		return method(ctx, unmarshal)
	}
}

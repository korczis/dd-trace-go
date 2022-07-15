// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022 Datadog, Inc.

// Package gqlgen contains an implementation of a gqlgen tracer, and functions to construct and configure the tracer.
// The tracer can be passed to the gqlgen handler (see package github.com/99designs/gqlgen/handler)
package gqlgen

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

const (
	graphQLQuery = "graphql.query"

	readOp       = "graphql.read"
	parsingOp    = "graphql.parse"
	validationOp = "graphql.validate"
)

type gqlTracer struct {
	cfg *config
}

// NewTracer creates a graphql.HandlerExtension instance that can be used with
// a graphql.handler.Server.
// Options can be passed in for further configuration.
func NewTracer(opts ...Option) graphql.HandlerExtension {
	cfg := new(config)
	defaults(cfg)
	for _, fn := range opts {
		fn(cfg)
	}
	return &gqlTracer{cfg: cfg}
}

func (t *gqlTracer) ExtensionName() string {
	return "DatadogTracing"
}

func (t *gqlTracer) Validate(schema graphql.ExecutableSchema) error {
	return nil // unimplemented
}

func (t *gqlTracer) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	opts := []ddtrace.StartSpanOption{
		tracer.SpanType(ext.SpanTypeGraphQL),
		tracer.ServiceName(t.cfg.serviceName),
	}
	if !math.IsNaN(t.cfg.analyticsRate) {
		opts = append(opts, tracer.Tag(ext.EventSampleRate, t.cfg.analyticsRate))
	}
	var (
		octx *graphql.OperationContext
	)
	name := ext.SpanTypeGraphQL
	if graphql.HasOperationContext(ctx) {
		octx = graphql.GetOperationContext(ctx)
		if octx.Operation != nil {
			if octx.Operation.Operation == ast.Subscription {
				// These are long running queries for a subscription,
				// remaining open indefinitely until a subscription ends.
				// Return early and do not create these spans.
				return next(ctx)
			}
			name = fmt.Sprintf("%s.%s", ext.SpanTypeGraphQL, octx.Operation.Operation)
		}
		opts = append(opts, tracer.ResourceName(octx.OperationName))
		if octx.RawQuery != "" {
			opts = append(opts, tracer.Tag(graphQLQuery, octx.RawQuery))
		}
		for key, val := range octx.Variables {
			opts = append(opts, tracer.Tag(fmt.Sprintf("graphql.variables.%s", key), val))
		}
		opts = append(opts, tracer.StartTime(octx.Stats.OperationStart))
	}
	if s, ok := tracer.SpanFromContext(ctx); ok {
		opts = append(opts, tracer.ChildOf(s.Context()))
	}
	opts = append(opts, opts...)
	var span ddtrace.Span
	span, ctx = tracer.StartSpanFromContext(ctx, name, opts...)
	defer func() {
		var finishOpts []ddtrace.FinishOption
		for _, err := range graphql.GetErrors(ctx) {
			finishOpts = append(finishOpts, tracer.WithError(err))
		}
		span.Finish(finishOpts...)
	}()

	if octx != nil {
		// Create child spans based on the stats in the operation context.
		createChildSpan := func(name string, start, finish time.Time) {
			var childOpts []ddtrace.StartSpanOption
			childOpts = append(childOpts, tracer.StartTime(start))
			childOpts = append(childOpts, tracer.ResourceName(name))
			var childSpan ddtrace.Span
			childSpan, _ = tracer.StartSpanFromContext(ctx, name, childOpts...)
			childSpan.Finish(tracer.FinishTime(finish))
		}
		createChildSpan(readOp, octx.Stats.Read.Start, octx.Stats.Read.End)
		createChildSpan(parsingOp, octx.Stats.Parsing.Start, octx.Stats.Parsing.End)
		createChildSpan(validationOp, octx.Stats.Validation.Start, octx.Stats.Validation.End)
	}
	return next(ctx)
}

// Ensure all of these interfaces are implemented.
var _ interface {
	graphql.HandlerExtension
	graphql.ResponseInterceptor
} = &gqlTracer{}

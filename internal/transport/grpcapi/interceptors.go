package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
)

// anonymousMethods is the complete allow-list of RPCs reachable without a session
// (the gRPC counterpart of auth.anonymousPaths). Everything else, including
// streaming and reflection RPCs, requires an authenticated principal. The
// allow-list is pinned by a test; adding to it is a security decision.
var anonymousMethods = map[string]struct{}{
	healthCheck: {},
}

// healthCheck is the full method name of the standard health check,
// which orchestrators call without credentials.
const healthCheck = "/grpc.health.v1.Health/Check" 

// AnonymousMethods returns a copy of the allow-list, for tests.
func AnonymousMethods() []string {
	out := make([]string, 0, len(anonymousMethods))
	for m := range anonymousMethods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// principal authenticates the call. Incoming metadata is HTTP/2 headers, so the
// same Authenticator used for HTTP (session cookie) works for gRPC clients that
// send a `cookie` metadata entry.
func principal(ctx context.Context, authn auth.Authenticator, method string) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	h := make(http.Header, len(md))
	for k, vs := range md {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if p, ok := authn.Authenticate(&http.Request{Header: h}); ok {
		return auth.WithPrincipal(ctx, p), nil
	}
	if _, ok := anonymousMethods[method]; ok {
		return ctx, nil
	}
	return ctx, status.Error(grpccodes.Unauthenticated, "Authentication is required.")
}

func authUnary(authn auth.Authenticator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, err := principal(ctx, authn, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func authStream(authn auth.Authenticator) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		ctx, err := principal(ss.Context(), authn, info.FullMethod)
		if err != nil {
			return err
		}
		return next(srv, ctxStream{ServerStream: ss, ctx: ctx})
	}
}

type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

// recoverUnary / recoverStream turn panics into the same generic INTERNAL as any
// other unexpected failure.
func recoverUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = unexpected(ctx, log, info.FullMethod, fmt.Errorf("panic: %v\n%s", rec, debug.Stack()))
			}
		}()
		return next(ctx, req)
	}
}

func recoverStream(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = unexpected(ss.Context(), log, info.FullMethod, fmt.Errorf("panic: %v\n%s", rec, debug.Stack()))
			}
		}()
		return next(srv, ss)
	}
}

// translateErrors maps the services' expected outcomes to the statuses the
// contract documents. Anything unrecognised is logged and masked.
func translateErrors(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		resp, err := next(ctx, req)
		if err == nil {
			return resp, nil
		}
		var ve domain.ValidationError
		switch {
		case errors.As(err, &ve):
			return nil, validationStatus(ve)
		case errors.Is(err, domain.ErrNotFound):
			return nil, status.Error(grpccodes.NotFound, "Not found.")
		}
		if _, ok := status.FromError(err); ok {
			return nil, err // already a status (e.g. from an interceptor)
		}
		return nil, unexpected(ctx, log, info.FullMethod, err)
	}
}

// validationStatus is INVALID_ARGUMENT with one google.rpc.BadRequest.FieldViolation
// per message, field = the property name used by the other transports.
func validationStatus(ve domain.ValidationError) error {
	fields := make([]string, 0, len(ve))
	for f := range ve {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	br := &errdetails.BadRequest{}
	for _, f := range fields {
		for _, m := range ve[f] {
			br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{Field: f, Description: m})
		}
	}
	st, err := status.New(grpccodes.InvalidArgument, "The request violates the business rules.").WithDetails(br)
	if err != nil {
		return status.Error(grpccodes.InvalidArgument, "The request violates the business rules.")
	}
	return st.Err()
}

func unexpected(ctx context.Context, log *slog.Logger, method string, err error) error {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, "unhandled error")
	log.ErrorContext(ctx, "unhandled error", "method", method, "error", err)
	return status.Error(grpccodes.Internal, "An unexpected error occurred.")
}

// badID is the violation for a malformed identifier.
func badID() error {
	return domain.ValidationError{"id": {"id must be a valid UUID"}}
}

package grpcapi

import (
	"log/slog"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	todosv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/todos/v1"
	usersv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/users/v1"
	"github.com/bjornnorgaard/golden-path-go/internal/todos"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

// Options are the dependencies of the gRPC server.
type Options struct {
	Todos *todos.Service
	Users *users.Service
	Authn auth.Authenticator
	Log   *slog.Logger
	// Reflection registers the gRPC reflection service (the gRPC counterpart of the
	// API docs). It is still subject to authentication.
	Reflection bool
}

// NewServer builds the gRPC server: every RPC goes through panic recovery,
// deny-by-default authentication and error translation, exactly like the REST
// and GraphQL transports, and the adapters below contain no business rules.
func NewServer(o Options) *grpc.Server {
	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			recoverUnary(o.Log),
			authUnary(o.Authn),
			translateErrors(o.Log),
		),
		grpc.ChainStreamInterceptor(
			recoverStream(o.Log),
			authStream(o.Authn),
		),
	)
	todosv1.RegisterTodosServiceServer(srv, &todosServer{svc: o.Todos})
	usersv1.RegisterUsersServiceServer(srv, &usersServer{svc: o.Users})
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	if o.Reflection {
		reflection.RegisterV1(srv)
	}
	return srv
}

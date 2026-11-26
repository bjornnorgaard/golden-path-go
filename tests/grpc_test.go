package tests

import (
	"context"
	"net"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bjornnorgaard/golden-path-go/internal/app"
	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi"
	todosv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/todos/v1"
	usersv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/users/v1"
)

// grpcClient is a connection to the in-process gRPC server. The same application
// wiring serves it as production (interceptors, authentication, services).
type grpcClient struct {
	t    testing.TB
	conn *grpc.ClientConn
	md   metadata.MD
}

func dialGRPC(t testing.TB, e *env, md metadata.MD) *grpcClient {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return e.grpcLis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &grpcClient{t: t, conn: conn, md: md}
}

// grpcAuthed acts as the test user; grpcAnonymous sends no credentials.
func grpcAuthed(t testing.TB) *grpcClient {
	return dialGRPC(t, suite, metadata.Pairs(authHeader, "1"))
}
func grpcAnonymous(t testing.TB) *grpcClient { return dialGRPC(t, suite, nil) }

func (c *grpcClient) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.t.Cleanup(cancel)
	return metadata.NewOutgoingContext(ctx, c.md)
}

func wantCode(t testing.TB, err error, want codes.Code) *status.Status {
	t.Helper()
	st, _ := status.FromError(err)
	if st.Code() != want {
		t.Fatalf("code = %s (%v), want %s", st.Code(), err, want)
	}
	return st
}

// violations extracts the documented google.rpc.BadRequest detail as field -> messages.
func violations(t testing.TB, st *status.Status) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				out[v.GetField()] = append(out[v.GetField()], v.GetDescription())
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("status %v carries no BadRequest detail", st)
	}
	return out
}

func TestGRPC_TodoLifecycle(t *testing.T) {
	t.Parallel()
	c := grpcAuthed(t)
	todos := todosv1.NewTodosServiceClient(c.conn)
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)

	created, err := todos.CreateTodo(c.ctx(), &todosv1.CreateTodoRequest{Title: "created over grpc", DueBy: timestamppb.New(due)})
	if err != nil {
		t.Fatal(err)
	}
	todo := created.GetTodo()
	if todo.GetTitle() != "created over grpc" || todo.GetIsComplete() || !todo.GetDueBy().AsTime().Equal(due) {
		t.Fatalf("unexpected: %v", todo)
	}
	todoID := uuid.MustParse(todo.GetId())
	if n := countJobs(t, "todo_due_reminder", todoID); n != 1 {
		t.Fatalf("reminder jobs = %d, want 1 (jobs are enqueued by the shared service)", n)
	}

	got, err := todos.GetTodoById(c.ctx(), &todosv1.GetTodoByIdRequest{Id: todo.GetId()})
	if err != nil || got.GetTodo().GetTitle() != todo.GetTitle() {
		t.Fatalf("get = %v, %v", got, err)
	}

	list, err := todos.GetTodoList(c.ctx(), &todosv1.GetTodoListRequest{})
	if err != nil || len(list.GetTodos()) == 0 {
		t.Fatalf("list = %v, %v", list, err)
	}

	updated, err := todos.UpdateTodo(c.ctx(), &todosv1.UpdateTodoRequest{Id: todo.GetId(), Title: "renamed over grpc"})
	if err != nil || updated.GetTodo().GetTitle() != "renamed over grpc" || updated.GetTodo().GetDueBy() != nil {
		t.Fatalf("update = %v, %v", updated, err)
	}

	toggled, err := todos.ToggleTodo(c.ctx(), &todosv1.ToggleTodoRequest{Id: todo.GetId()})
	if err != nil || !toggled.GetTodo().GetIsComplete() {
		t.Fatalf("toggle = %v, %v", toggled, err)
	}
	if n := countJobs(t, "todo_completed_notification", todoID); n != 1 {
		t.Fatalf("completion notification jobs = %d, want 1 (all transports share the service)", n)
	}

	if deleted, err := todos.DeleteTodo(c.ctx(), &todosv1.DeleteTodoRequest{Id: todo.GetId()}); err != nil || deleted.GetId() != todo.GetId() {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	_, err = todos.GetTodoById(c.ctx(), &todosv1.GetTodoByIdRequest{Id: todo.GetId()})
	wantCode(t, err, codes.NotFound)
}

// A todo created over gRPC is the same row REST serves: one service, one database.
func TestGRPC_AndRESTShareTheSameData(t *testing.T) {
	t.Parallel()
	c := grpcAuthed(t)
	created, err := todosv1.NewTodosServiceClient(c.conn).CreateTodo(c.ctx(), &todosv1.CreateTodoRequest{Title: "visible over rest"})
	if err != nil {
		t.Fatal(err)
	}
	r := authed(t).do("POST", "/todos/get-by-id", map[string]any{"id": created.GetTodo().GetId()})
	if r.Status != 200 {
		t.Fatalf("REST get = %d; body: %s", r.Status, r.Body)
	}
}

func TestGRPC_Todos_InvalidInputAndNotFound(t *testing.T) {
	t.Parallel()
	c := grpcAuthed(t)
	todos := todosv1.NewTodosServiceClient(c.conn)
	missing := uuid.NewString()

	t.Run("business rule violation is INVALID_ARGUMENT with a BadRequest detail", func(t *testing.T) {
		_, err := todos.CreateTodo(c.ctx(), &todosv1.CreateTodoRequest{Title: "ab"})
		if v := violations(t, wantCode(t, err, codes.InvalidArgument)); len(v["title"]) == 0 {
			t.Fatalf("violations = %v, want one for title", v)
		}
	})
	t.Run("malformed id is reported against id", func(t *testing.T) {
		for name, call := range map[string]func() error{
			"get": func() error { _, e := todos.GetTodoById(c.ctx(), &todosv1.GetTodoByIdRequest{Id: "nope"}); return e },
			"update": func() error {
				_, e := todos.UpdateTodo(c.ctx(), &todosv1.UpdateTodoRequest{Id: "nope", Title: "long enough"})
				return e
			},
			"toggle": func() error { _, e := todos.ToggleTodo(c.ctx(), &todosv1.ToggleTodoRequest{Id: "nope"}); return e },
			"delete": func() error { _, e := todos.DeleteTodo(c.ctx(), &todosv1.DeleteTodoRequest{Id: "nope"}); return e },
		} {
			if v := violations(t, wantCode(t, call(), codes.InvalidArgument)); len(v["id"]) == 0 {
				t.Fatalf("%s: violations = %v, want one for id", name, v)
			}
		}
	})
	t.Run("paging bounds are business rules too", func(t *testing.T) {
		neg := int32(-1)
		_, err := todos.GetTodoList(c.ctx(), &todosv1.GetTodoListRequest{Offset: &neg})
		wantCode(t, err, codes.InvalidArgument)
	})
	t.Run("unknown id is NOT_FOUND", func(t *testing.T) {
		for name, call := range map[string]func() error{
			"get": func() error { _, e := todos.GetTodoById(c.ctx(), &todosv1.GetTodoByIdRequest{Id: missing}); return e },
			"update": func() error {
				_, e := todos.UpdateTodo(c.ctx(), &todosv1.UpdateTodoRequest{Id: missing, Title: "long enough"})
				return e
			},
			"toggle": func() error { _, e := todos.ToggleTodo(c.ctx(), &todosv1.ToggleTodoRequest{Id: missing}); return e },
			"delete": func() error { _, e := todos.DeleteTodo(c.ctx(), &todosv1.DeleteTodoRequest{Id: missing}); return e },
		} {
			if err := call(); status.Code(err) != codes.NotFound {
				t.Fatalf("%s: code = %s, want NotFound", name, status.Code(err))
			}
		}
	})
}

func TestGRPC_Users(t *testing.T) {
	t.Parallel()
	c := grpcAuthed(t)
	users := usersv1.NewUsersServiceClient(c.conn)

	got, err := users.GetUserById(c.ctx(), &usersv1.GetUserByIdRequest{Id: testPrincipal.UserID.String()})
	if err != nil || got.GetUser().GetEmail() != testPrincipal.Email {
		t.Fatalf("get = %v, %v", got, err)
	}
	if list, err := users.GetUserList(c.ctx(), &usersv1.GetUserListRequest{}); err != nil || len(list.GetUsers()) == 0 {
		t.Fatalf("list = %v, %v", list, err)
	}

	// A user of its own, so renaming and deleting does not disturb the shared test principal.
	other := uuid.New()
	if _, err := suite.pool.Exec(context.Background(), `INSERT INTO users (id, email) VALUES ($1, $2)`, other, other.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	name := "Renamed"
	updated, err := users.UpdateUser(c.ctx(), &usersv1.UpdateUserRequest{Id: other.String(), DisplayName: &name})
	if err != nil || updated.GetUser().GetDisplayName() != name {
		t.Fatalf("update = %v, %v", updated, err)
	}
	if _, err := users.DeleteUser(c.ctx(), &usersv1.DeleteUserRequest{Id: other.String()}); err != nil {
		t.Fatal(err)
	}
	_, err = users.GetUserById(c.ctx(), &usersv1.GetUserByIdRequest{Id: other.String()})
	wantCode(t, err, codes.NotFound)
	_, err = users.GetUserById(c.ctx(), &usersv1.GetUserByIdRequest{Id: "nope"})
	wantCode(t, err, codes.InvalidArgument)
}

// Deny-by-default: every RPC the server registers rejects anonymous callers unless it is allow-listed.
func TestGRPC_EveryMethodRequiresAuthenticationUnlessAllowListed(t *testing.T) {
	t.Parallel()
	c := grpcAnonymous(t)
	allowed := grpcapi.AnonymousMethods()

	var methods []string
	streaming := map[string]bool{}
	for svc, info := range suite.app.GRPC().GetServiceInfo() {
		for _, m := range info.Methods {
			full := "/" + svc + "/" + m.Name
			methods = append(methods, full)
			streaming[full] = m.IsClientStream || m.IsServerStream
		}
	}
	sort.Strings(methods)
	if len(methods) == 0 {
		t.Fatal("no gRPC methods registered")
	}
	for _, full := range methods {
		if slices.Contains(allowed, full) {
			continue
		}
		t.Run(full, func(t *testing.T) {
			var err error
			if streaming[full] {
				var s grpc.ClientStream
				if s, err = c.conn.NewStream(c.ctx(), &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, full); err == nil {
					_ = s.CloseSend()
					err = s.RecvMsg(&emptypb.Empty{})
				}
			} else {
				err = c.conn.Invoke(c.ctx(), full, &emptypb.Empty{}, &emptypb.Empty{})
			}
			wantCode(t, err, codes.Unauthenticated)
		})
	}
}

// Pins the allow-list: widening anonymous access must be a deliberate, reviewed change to this test.
func TestGRPC_AnonymousAllowListIsPinned(t *testing.T) {
	t.Parallel()
	got := grpcapi.AnonymousMethods()
	want := []string{"/grpc.health.v1.Health/Check"}
	if !slices.Equal(got, want) {
		t.Fatalf("anonymous methods = %v, want %v", got, want)
	}
}

func TestGRPC_HealthCheckIsAnonymous(t *testing.T) {
	t.Parallel()
	resp, err := grpc_health_v1.NewHealthClient(grpcAnonymous(t).conn).Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", resp, err)
	}
}

// The production authenticator (signed session cookies) works over gRPC: the cookie is sent as metadata.
func TestGRPC_SessionCookie_AuthenticatesAgainstTheRealApp(t *testing.T) {
	t.Parallel()
	e, err := newEnv(context.Background(), nil, func(o *app.Options) { o.Authenticator = nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	sessions := auth.NewSessions(sessionSecret, time.Hour, false)
	good := sessions.Encode(testPrincipal)

	call := func(cookie string) error {
		md := metadata.MD{}
		if cookie != "" {
			md = metadata.Pairs("cookie", auth.CookieName+"="+cookie)
		}
		c := dialGRPC(t, e, md)
		_, err := todosv1.NewTodosServiceClient(c.conn).GetTodoList(c.ctx(), &todosv1.GetTodoListRequest{})
		return err
	}
	if err := call(good); err != nil {
		t.Fatalf("valid cookie: %v", err)
	}
	for name, cookie := range map[string]string{
		"no cookie": "",
		"tampered":  good[:len(good)-2] + "xx",
		"expired":   auth.NewSessions(sessionSecret, -time.Minute, false).Encode(testPrincipal),
	} {
		if status.Code(call(cookie)) != codes.Unauthenticated {
			t.Fatalf("%s: want Unauthenticated, got %v", name, call(cookie))
		}
	}
}

// Reflection is the gRPC counterpart of the docs UI: off by default, on with DOCS_ENABLED, never anonymous.
func TestGRPC_Reflection_FollowsDocsEnabled(t *testing.T) {
	t.Parallel()
	services := func(e *env, md metadata.MD) ([]string, error) {
		c := dialGRPC(t, e, md)
		stream, err := grpc_reflection_v1.NewServerReflectionClient(c.conn).ServerReflectionInfo(c.ctx())
		if err != nil {
			return nil, err
		}
		if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{}}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		var names []string
		for _, s := range resp.GetListServicesResponse().GetService() {
			names = append(names, s.GetName())
		}
		return names, nil
	}

	if _, err := services(suite, metadata.Pairs(authHeader, "1")); status.Code(err) != codes.Unimplemented {
		t.Fatalf("reflection with docs disabled: %v, want Unimplemented", err)
	}

	e, err := newEnv(context.Background(), func(c *config.Config) { c.DocsEnabled = true })
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	if _, err := services(e, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous reflection: %v, want Unauthenticated", err)
	}
	names, err := services(e, metadata.Pairs(authHeader, "1"))
	if err != nil || !slices.Contains(names, "goldenpath.todos.v1.TodosService") {
		t.Fatalf("authenticated reflection = %v, %v", names, err)
	}
}

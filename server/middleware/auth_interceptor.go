package middleware

import (
	"context"
	"strings"

	"github.com/feralbureau/bedrock-api/pkg/token"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// context_key is a custom type for context keys to avoid collisions.
type contextKey string

const (
	userIDKey   contextKey = "user_id"
	userRoleKey contextKey = "user_role"
)

// auth_interceptor protects grpc server with jwt authorization.
type AuthInterceptor struct {
	jwtManager    *token.JWTManager
	publicMethods map[string]bool
}

// new_auth_interceptor creates a new auth interceptor.
func NewAuthInterceptor(jwtManager *token.JWTManager, publicMethods map[string]bool) *AuthInterceptor {
	return &AuthInterceptor{
		jwtManager:    jwtManager,
		publicMethods: publicMethods,
	}
}

// unary returns a server interceptor function to authenticate unary rpc.
func (i *AuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if i.publicMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		authCtx, err := i.authorize(ctx)
		if err != nil {
			return nil, err
		}

		return handler(authCtx, req)
	}
}

// stream returns a server interceptor function to authenticate stream rpc.
func (i *AuthInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if i.publicMethods[info.FullMethod] {
			return handler(srv, stream)
		}

		authCtx, err := i.authorize(stream.Context())
		if err != nil {
			return err
		}

		wrappedStream := &wrappedServerStream{
			ServerStream: stream,
			ctx:          authCtx,
		}

		return handler(srv, wrappedStream)
	}
}

// authorize extracts and validates the jwt token from the context.
func (i *AuthInterceptor) authorize(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "metadata is not provided")
	}

	values := md["authorization"]
	if len(values) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "authorization token is not provided")
	}

	authHeader := values[0]
	// expect format: "Bearer <token>"
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, status.Errorf(codes.Unauthenticated, "authorization header format must be bearer <token>")
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := i.jwtManager.Validate(tokenStr)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "access token is invalid: %v", err)
	}

	// inject claims back into the context
	ctx = context.WithValue(ctx, userIDKey, claims.UserID)
	ctx = context.WithValue(ctx, userRoleKey, claims.Role)

	return ctx, nil
}

// wrapped_server_stream wraps grpc.serverstream to allow modifying context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// context returns the modified context.
func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// get_user_id extracts the user id from context.
func GetUserID(ctx context.Context) string {
	if val, ok := ctx.Value(userIDKey).(string); ok {
		return val
	}
	return ""
}

// get_user_role extracts the user role from context.
func GetUserRole(ctx context.Context) string {
	if val, ok := ctx.Value(userRoleKey).(string); ok {
		return val
	}
	return ""
}

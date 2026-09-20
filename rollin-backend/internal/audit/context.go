package audit

import "context"

// RequestInfo carries the per-request metadata the audit rows want (04 §5.15 lists
// request_id/ip_address). The HTTP middleware stamps it into the request context via
// WithRequestInfo; services read it with FromContext when composing Entries, so the
// service signatures never have to grow IP/request-id parameters.
type RequestInfo struct {
	RequestID string
	IPAddress string
	UserAgent string
}

type requestInfoKey struct{}

// WithRequestInfo stores the request metadata in ctx (middleware side).
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// FromContext extracts the request metadata; the zero value (everything empty) is valid
// for callers outside HTTP (workers, bootstrap), where those columns simply stay NULL.
func FromContext(ctx context.Context) RequestInfo {
	if info, ok := ctx.Value(requestInfoKey{}).(RequestInfo); ok {
		return info
	}
	return RequestInfo{}
}

package httpx

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

// RouteSet is a declarative route table.
//
// Routes are DATA rather than imperative registration calls, which is what
// makes them diffable against the OpenAPI spec in a test (AssertNoDrift). The
// current middleware silently passes a request whose route is absent from the
// spec, so drift has had nothing to catch it.
type RouteSet struct {
	// Prefix is prepended to every route's path, relative to the router's Base.
	Prefix string
	Routes []Route
}

// Route is one endpoint.
type Route struct {
	Method string
	Path   string
	// Handler comes from Handle, HandleVoid or HandleRaw. It is a plain
	// http.HandlerFunc so the router backend stays swappable — nothing in a
	// service knows which router is mounted underneath.
	Handler http.HandlerFunc
	// Roles restricts the route to callers holding at least one of them. Empty
	// means any authenticated subject.
	Roles []string
	// Public bypasses authentication entirely. Name it explicitly at the route
	// rather than by path convention: a route that is public by accident is the
	// kind of mistake nobody notices in review.
	Public bool
	// Optional serves anonymous callers but still resolves identity when one
	// is supplied, so the handler can widen its result.
	//
	// Distinct from Public, which means "no identity expected at all" — a JWKS
	// document or a health probe. Conflating them is how a public read ends up
	// anonymous even for a signed-in caller.
	Optional bool
	// OpID is the OpenAPI operationId, used by AssertNoDrift.
	OpID string
}

// AuthSpec is the router's authentication wiring.
//
// One type replacing `type AuthConfig struct{SharedSecret string; JWT ...}`,
// which is currently redeclared in 17 service files.
type AuthSpec struct {
	// Authenticate verifies the caller and puts a Subject on the context. It is
	// supplied rather than constructed here so platform/http stays free of any
	// dependency on JWT or HMAC verification — an edge package should route and
	// render, not decide what a valid credential is.
	Authenticate func(http.Handler) http.Handler
	// AllowAnonymous lets an unauthenticated request reach a non-Public route.
	//
	// It is spelled as an opt-OUT so the zero value is the safe one: a service
	// that forgets to configure this gets authentication enforced, not skipped.
	// A bool named RequireAuth would default to false and quietly open every
	// route in the group.
	AllowAnonymous bool
}

// RouterSpec configures NewRouter.
type RouterSpec struct {
	// Base is the API base path, e.g. "/iudx/v2/acl".
	Base string
	// URNs is the service's URN namespace token, e.g. "acl".
	URNs URNSpace
	// Health is mounted at /healthz/live and /healthz/ready.
	Health *health.Registry
	// Metrics is mounted at /metrics when non-nil.
	Metrics http.Handler
	// Docs is mounted at DocsPath when non-nil.
	Docs     http.Handler
	DocsPath string
	// Auth wires authentication onto Base. Routes marked Public bypass it.
	Auth AuthSpec
	// Mappers are error mappers passed to every handler built by Routes.
	Mappers []ErrorMapper
	// Middleware runs on every request, after the platform's own stack.
	Middleware []func(http.Handler) http.Handler
	Logger     *zap.Logger
}

// NewRouter builds the standard router: the platform middleware stack, the
// operational endpoints, then the service's routes under Base with
// authentication applied.
//
// It replaces, per service: a redeclared AuthConfig (17 files), an identical
// okHandler (13), an identical 5-line auth-resolver block (19 places), and a
// hand-wired 7-line middleware stack (14 services).
//
// Tracing is not an option and not a WithTracing() opt-in. That opt-in is
// precisely why 13 of 18 services run with no tracing middleware today: an
// observability default that must be asked for is an observability default
// that will be forgotten.
func NewRouter(spec RouterSpec, sets ...RouteSet) http.Handler {
	if spec.Logger == nil {
		spec.Logger = zap.NewNop()
	}
	r := chi.NewRouter()

	// The router backend reports path parameters; nothing else in the platform
	// knows chi is here.
	SetPathValueFunc(func(req *http.Request, name string) string {
		return chi.URLParam(req, name)
	})

	// Recovery FIRST, so it wraps every other middleware and every handler. A
	// panic in a later middleware is just as fatal as one in a handler.
	r.Use(recoverPanics(spec.Logger))

	for _, mw := range spec.Middleware {
		r.Use(mw)
	}

	// Operational endpoints sit OUTSIDE Base and outside authentication:
	// kubelet does not carry a bearer token, and a readiness probe that needs
	// one fails closed for the wrong reason.
	if spec.Health != nil {
		r.Get("/healthz/live", spec.Health.Live())
		r.Get("/healthz/ready", spec.Health.Ready())
	}
	if spec.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", spec.Metrics)
	}
	if spec.Docs != nil {
		path := spec.DocsPath
		if path == "" {
			path = "/docs"
		}
		// StripPrefix, so the handler sees paths RELATIVE to where it is
		// mounted — "/" and "/openapi.json" rather than "/docs" and
		// "/docs/openapi.json".
		//
		// chi's Mount alone does not do this: it records the remainder on the
		// route context but leaves r.URL.Path untouched, so any handler that
		// routes on URL.Path (a http.ServeMux, openapi.Handler, anything not
		// chi) matched nothing and 404'd on every docs route. A mounted chi
		// sub-router is unaffected — it reads the route context, not the URL.
		r.Mount(path, http.StripPrefix(strings.TrimSuffix(path, "/"), spec.Docs))
	}

	base := spec.Base
	if base == "" {
		base = "/"
	}

	r.Route(base, func(br chi.Router) {
		public, protected := splitRoutes(sets)

		for _, rt := range public {
			br.Method(rt.Method, rt.Path, rt.Handler)
		}

		if len(protected) == 0 {
			return
		}
		br.Group(func(pr chi.Router) {
			if spec.Auth.Authenticate != nil {
				pr.Use(spec.Auth.Authenticate)
			}
			for _, rt := range protected {
				h := rt.Handler
				if rt.Optional {
					// No subject gate: anonymity is the point. The resolver
					// still ran, so an identified caller reaches the handler
					// with their Subject on the context.
					pr.Method(rt.Method, rt.Path, h)
					continue
				}
				if len(rt.Roles) > 0 {
					h = requireRoles(h, rt.Roles, spec.Mappers, spec.Logger)
				} else if !spec.Auth.AllowAnonymous {
					// Enforce a verified subject at the ROUTE, not by relying on
					// the handler's request type embedding Actor. Otherwise a
					// handler that happens not to need the caller's identity —
					// one taking httpx.None — is silently public despite sitting
					// in the protected group. That is precisely the "public by
					// accident" mistake nobody catches in review.
					h = requireSubject(h, spec.Mappers, spec.Logger)
				}
				pr.Method(rt.Method, rt.Path, h)
			}
		})
	})

	return r
}

// splitRoutes separates public from protected routes, flattening each set's
// prefix into the route path.
func splitRoutes(sets []RouteSet) (public, protected []Route) {
	for _, set := range sets {
		for _, rt := range set.Routes {
			rt.Path = joinPath(set.Prefix, rt.Path)
			if rt.Public {
				public = append(public, rt)
			} else {
				protected = append(protected, rt)
			}
		}
	}
	return public, protected
}

func joinPath(prefix, path string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	switch {
	case path == "" || path == "/":
		if prefix == "" {
			return "/"
		}
		return prefix
	case !strings.HasPrefix(path, "/"):
		path = "/" + path
	}
	return prefix + path
}

// Routes is a small helper for assembling a RouteSet.
func Routes(prefix string, routes ...Route) RouteSet {
	return RouteSet{Prefix: prefix, Routes: routes}
}

// GET/POST/PUT/PATCH/DELETE build a Route. They keep a route table readable as
// a table rather than as five-field struct literals.
func GET(path string, h http.HandlerFunc, opts ...RouteOption) Route {
	return route(http.MethodGet, path, h, opts)
}
func POST(path string, h http.HandlerFunc, opts ...RouteOption) Route {
	return route(http.MethodPost, path, h, opts)
}
func PUT(path string, h http.HandlerFunc, opts ...RouteOption) Route {
	return route(http.MethodPut, path, h, opts)
}
func PATCH(path string, h http.HandlerFunc, opts ...RouteOption) Route {
	return route(http.MethodPatch, path, h, opts)
}
func DELETE(path string, h http.HandlerFunc, opts ...RouteOption) Route {
	return route(http.MethodDelete, path, h, opts)
}

// RouteOption configures a Route.
type RouteOption func(*Route)

// Roles restricts a route to callers holding at least one of them.
func Roles(roles ...string) RouteOption {
	return func(r *Route) { r.Roles = roles }
}

// Public marks a route as unauthenticated — no identity is resolved or
// expected. For a route that should serve anonymous callers but still see a
// caller when one is present, use Optional.
func Public() RouteOption { return func(r *Route) { r.Public = true } }

// Optional marks a route that serves anonymous callers AND sees a verified
// caller when one is supplied.
//
// Its handler must take a request embedding httpx.OptionalActor and be built
// with HandleOptional; Handle rejects such a request type at construction, so
// a mismatch fails at boot rather than leaking at runtime.
func Optional() RouteOption { return func(r *Route) { r.Optional = true } }

// OpID records the OpenAPI operationId, for the spec-drift test.
func OpID(id string) RouteOption { return func(r *Route) { r.OpID = id } }

func route(method, path string, h http.HandlerFunc, opts []RouteOption) Route {
	rt := Route{Method: method, Path: path, Handler: h}
	for _, o := range opts {
		o(&rt)
	}
	return rt
}

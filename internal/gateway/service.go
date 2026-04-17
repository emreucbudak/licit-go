package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/licit/licit-go/internal/config"
)

type Service struct {
	routes              []config.GatewayRouteConfig
	clusters            map[string]*Cluster
	proxy               *httputil.ReverseProxy
	client              *http.Client
	healthCheckInterval time.Duration
	healthCheckTimeout  time.Duration
}

type Cluster struct {
	name       string
	policy     string
	healthPath string
	backends   []*Backend
	next       uint64
}

type Backend struct {
	target  *url.URL
	healthy atomic.Bool

	mu             sync.RWMutex
	lastChecked    time.Time
	lastError      string
	lastStatusCode int
}

type healthResponse struct {
	Status   string                     `json:"status"`
	Clusters map[string]clusterSnapshot `json:"clusters"`
}

type clusterSnapshot struct {
	Healthy  bool              `json:"healthy"`
	Backends []backendSnapshot `json:"backends"`
}

type backendSnapshot struct {
	URL            string    `json:"url"`
	Healthy        bool      `json:"healthy"`
	LastChecked    time.Time `json:"last_checked,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastStatusCode int       `json:"last_status_code,omitempty"`
}

type backendContextKey struct{}

func New(cfg config.GatewayConfig) (*Service, error) {
	if len(cfg.Routes) == 0 {
		return nil, errors.New("gateway routes are required")
	}

	if len(cfg.Clusters) == 0 {
		return nil, errors.New("gateway clusters are required")
	}

	clusters := make(map[string]*Cluster, len(cfg.Clusters))
	for name, clusterCfg := range cfg.Clusters {
		cluster, err := newCluster(name, clusterCfg)
		if err != nil {
			return nil, err
		}
		clusters[name] = cluster
	}

	for _, route := range cfg.Routes {
		if strings.TrimSpace(route.Name) == "" {
			return nil, errors.New("gateway route name is required")
		}

		if strings.TrimSpace(route.Path) == "" {
			return nil, fmt.Errorf("gateway route %s path is required", route.Name)
		}

		switch route.Match {
		case "exact", "prefix", "catch_all":
		default:
			return nil, fmt.Errorf("gateway route %s has unsupported match type %q", route.Name, route.Match)
		}

		if _, ok := clusters[route.Cluster]; !ok {
			return nil, fmt.Errorf("gateway route %s references unknown cluster %q", route.Name, route.Cluster)
		}
	}

	timeout := cfg.CheckTimeout()
	service := &Service{
		routes:              cfg.Routes,
		clusters:            clusters,
		client:              &http.Client{Timeout: timeout},
		healthCheckInterval: cfg.CheckInterval(),
		healthCheckTimeout:  timeout,
	}

	service.proxy = &httputil.ReverseProxy{
		Rewrite:       service.rewriteRequest,
		ErrorHandler:  service.handleProxyError,
		FlushInterval: 100 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}

	return service, nil
}

func (s *Service) Start(ctx context.Context) {
	s.runHealthChecks(ctx)

	ticker := time.NewTicker(s.healthCheckInterval)
	go func() {
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runHealthChecks(ctx)
			}
		}
	}()
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/backends", s.handleBackendHealth)
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/", s)

	return mux
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := s.matchRoute(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return
	}

	cluster := s.clusters[route.Cluster]
	backend, ok := cluster.NextBackend()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":   "no healthy backends available",
			"cluster": route.Cluster,
		})
		return
	}

	ctx := context.WithValue(r.Context(), backendContextKey{}, backend)
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Service) matchRoute(path string) (config.GatewayRouteConfig, bool) {
	for _, route := range s.routes {
		switch route.Match {
		case "exact":
			if path == route.Path {
				return route, true
			}
		case "prefix":
			if prefixMatch(route.Path, path) {
				return route, true
			}
		case "catch_all":
			return route, true
		}
	}

	return config.GatewayRouteConfig{}, false
}

func (s *Service) rewriteRequest(pr *httputil.ProxyRequest) {
	backend, ok := pr.In.Context().Value(backendContextKey{}).(*Backend)
	if !ok || backend == nil {
		return
	}

	pr.SetURL(backend.target)
	pr.SetXForwarded()
}

func (s *Service) handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	if backend, ok := r.Context().Value(backendContextKey{}).(*Backend); ok && backend != nil {
		backend.setHealth(false, err.Error(), 0)
	}

	slog.Error("gateway proxy error", "error", err, "path", r.URL.Path)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad gateway"})
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	statusCode, payload := s.snapshot()
	writeJSON(w, statusCode, payload)
}

func (s *Service) handleBackendHealth(w http.ResponseWriter, r *http.Request) {
	_, payload := s.snapshot()
	writeJSON(w, http.StatusOK, payload)
}

func (s *Service) snapshot() (int, healthResponse) {
	response := healthResponse{
		Status:   "ok",
		Clusters: make(map[string]clusterSnapshot, len(s.clusters)),
	}

	statusCode := http.StatusOK
	for name, cluster := range s.clusters {
		snapshot := cluster.Snapshot()
		if !snapshot.Healthy {
			response.Status = "degraded"
			statusCode = http.StatusServiceUnavailable
		}

		response.Clusters[name] = snapshot
	}

	return statusCode, response
}

func (s *Service) runHealthChecks(ctx context.Context) {
	var wg sync.WaitGroup

	for _, cluster := range s.clusters {
		for _, backend := range cluster.backends {
			wg.Add(1)

			go func(cluster *Cluster, backend *Backend) {
				defer wg.Done()
				s.checkBackend(ctx, cluster, backend)
			}(cluster, backend)
		}
	}

	wg.Wait()
}

func (s *Service) checkBackend(ctx context.Context, cluster *Cluster, backend *Backend) {
	checkCtx, cancel := context.WithTimeout(ctx, s.healthCheckTimeout)
	defer cancel()

	healthURL := backend.healthURL(cluster.healthPath)
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		backend.setHealth(false, err.Error(), 0)
		return
	}

	resp, err := s.client.Do(req)
	if err != nil {
		backend.setHealth(false, err.Error(), 0)
		return
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusInternalServerError
	if healthy {
		backend.setHealth(true, "", resp.StatusCode)
		return
	}

	backend.setHealth(false, fmt.Sprintf("health endpoint returned %d", resp.StatusCode), resp.StatusCode)
}

func newCluster(name string, cfg config.GatewayClusterConfig) (*Cluster, error) {
	if len(cfg.Destinations) == 0 {
		return nil, fmt.Errorf("cluster %s has no destinations", name)
	}

	policy := cfg.LoadBalancingPolicy
	if policy == "" {
		policy = "round_robin"
	}

	if policy != "round_robin" {
		return nil, fmt.Errorf("cluster %s has unsupported load balancing policy %q", name, policy)
	}

	healthPath := cfg.HealthPath
	if healthPath == "" {
		healthPath = "/health"
	}

	if !strings.HasPrefix(healthPath, "/") {
		healthPath = "/" + healthPath
	}

	cluster := &Cluster{
		name:       name,
		policy:     policy,
		healthPath: healthPath,
		backends:   make([]*Backend, 0, len(cfg.Destinations)),
	}

	for _, destination := range cfg.Destinations {
		target, err := url.Parse(destination)
		if err != nil {
			return nil, fmt.Errorf("cluster %s has invalid destination %q: %w", name, destination, err)
		}

		if target.Scheme == "" || target.Host == "" {
			return nil, fmt.Errorf("cluster %s destination %q must include scheme and host", name, destination)
		}

		cluster.backends = append(cluster.backends, &Backend{target: target})
	}

	return cluster, nil
}

func (c *Cluster) NextBackend() (*Backend, bool) {
	healthyBackends := make([]*Backend, 0, len(c.backends))
	for _, backend := range c.backends {
		if backend.IsHealthy() {
			healthyBackends = append(healthyBackends, backend)
		}
	}

	if len(healthyBackends) == 0 {
		return nil, false
	}

	index := int(atomic.AddUint64(&c.next, 1)-1) % len(healthyBackends)
	return healthyBackends[index], true
}

func (c *Cluster) Snapshot() clusterSnapshot {
	snapshot := clusterSnapshot{
		Healthy:  false,
		Backends: make([]backendSnapshot, 0, len(c.backends)),
	}

	for _, backend := range c.backends {
		backendSnapshot := backend.Snapshot()
		if backendSnapshot.Healthy {
			snapshot.Healthy = true
		}
		snapshot.Backends = append(snapshot.Backends, backendSnapshot)
	}

	return snapshot
}

func (b *Backend) IsHealthy() bool {
	return b.healthy.Load()
}

func (b *Backend) Snapshot() backendSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return backendSnapshot{
		URL:            b.target.String(),
		Healthy:        b.healthy.Load(),
		LastChecked:    b.lastChecked,
		LastError:      b.lastError,
		LastStatusCode: b.lastStatusCode,
	}
}

func (b *Backend) setHealth(healthy bool, lastError string, statusCode int) {
	b.healthy.Store(healthy)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastChecked = time.Now()
	b.lastError = lastError
	b.lastStatusCode = statusCode
}

func (b *Backend) healthURL(healthPath string) string {
	target := *b.target
	target.Path = joinPaths(target.Path, healthPath)
	target.RawQuery = ""
	target.Fragment = ""

	return target.String()
}

func prefixMatch(prefix, path string) bool {
	if prefix == "/" {
		return true
	}

	if path == prefix {
		return true
	}

	if !strings.HasPrefix(path, prefix) {
		return false
	}

	return len(path) > len(prefix) && path[len(prefix)] == '/'
}

func joinPaths(basePath, addPath string) string {
	switch {
	case basePath == "":
		return addPath
	case addPath == "":
		return basePath
	case strings.HasSuffix(basePath, "/") && strings.HasPrefix(addPath, "/"):
		return basePath + strings.TrimPrefix(addPath, "/")
	case !strings.HasSuffix(basePath, "/") && !strings.HasPrefix(addPath, "/"):
		return basePath + "/" + addPath
	default:
		return basePath + addPath
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("encode json response", "error", err)
	}
}

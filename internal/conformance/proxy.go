package conformance

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func BackendProxy(target *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[backend-tls] %s %s", r.Method, r.URL.Path)
		proxy.ServeHTTP(w, r)
	})
}
func ServeProxy(ctx context.Context, cert, key, port, upstream string) error {
	target, err := url.Parse(upstream)
	if err != nil || target.Host == "" {
		return fmt.Errorf("invalid backend URL %q", upstream)
	}
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: BackendProxy(target), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err = srv.ListenAndServeTLS(cert, key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

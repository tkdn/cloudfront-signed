package main

import "net/http"

// routeRegistrarはuploadServerのルーティング構築を差し替え可能にする。
// real環境ではrealRouteRegistrarがAWSを呼び出し、mock環境では
// mockRouteRegistrarがローカルファイルシステムのみで完結させる。
type routeRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

type uploadServerConfig struct {
	StaticDir string
	Routes    routeRegistrar
}

type uploadServer struct {
	cfg uploadServerConfig
	mux *http.ServeMux
}

func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	cfg.Routes.RegisterRoutes(s.mux)
	if cfg.StaticDir != "" {
		s.mux.Handle("/", http.FileServer(http.Dir(cfg.StaticDir)))
	}
	return s
}

func (s *uploadServer) ServeMux() *http.ServeMux {
	return s.mux
}

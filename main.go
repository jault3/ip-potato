package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

//go:embed templates/*.html
var htmlTemplates embed.FS
var templ *template.Template

//go:embed static/*
var staticFS embed.FS

func main() {
	listenAddr := flag.String("listen", "localhost:8080", "Listen address for the http server")
	flag.Parse()

	var err error
	templ, err = template.ParseFS(htmlTemplates, "templates/*.html")
	if err != nil {
		panic(err)
	}

	server := NewServer(*listenAddr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt)
	defer cancel()

	if err := ListenAndServe(ctx, server); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server did not shut down gracefully", slog.Any("error", err))
		panic(err)
	}
}

func NewServer(listenAddr string) *http.Server {
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(subFS)))
	mux.HandleFunc("GET /", handler())

	return &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}
}

// Runs the http server until the given context expires. Once expired, a graceful shutdown
// will be triggered with a timeout. This function always returns a non-nil error. After
// a successful graceful shutdown, the error will be http.ErrServerClosed.
func ListenAndServe(ctx context.Context, server *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Server successfully started", slog.String("addr", server.Addr))
		serverErr <- server.ListenAndServe()
	}()
	var err error
	select {
	case <-ctx.Done():
		timeout := 8 * time.Second
		slog.Info("Triggering graceful shutdown of the http server", slog.Duration("timeout", timeout))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err = server.Shutdown(shutdownCtx)
	case err = <-serverErr:
	}
	return err
}

func handler() http.HandlerFunc {
	acceptedMediaTypes := map[string]http.HandlerFunc{
		"text/html":        handleHTTPReq,
		"application/json": handleJSONReq,
	}
	return func(w http.ResponseWriter, req *http.Request) {
		accept := req.Header.Get("Accept")
		requestedMediaTypes := strings.Split(strings.Split(accept, ";")[0], ",")
		for _, mediaType := range requestedMediaTypes {
			if mediaTypeHandler, isMapped := acceptedMediaTypes[strings.TrimSpace(mediaType)]; isMapped {
				mediaTypeHandler(w, req)
				return
			}
		}
		handleTextReq(w, req)
	}
}

func handleHTTPReq(w http.ResponseWriter, req *http.Request) {
	err := templ.ExecuteTemplate(w, "index.html", map[string]string{
		"ip": realIP(req),
	})
	if err != nil {
		slog.Error("failed to render html template", slog.Any("error", err))
	}
}

func handleJSONReq(w http.ResponseWriter, req *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"ip": realIP(req),
	})
}

func handleTextReq(w http.ResponseWriter, req *http.Request) {
	w.Write([]byte(realIP(req) + "\n"))
}

// https://github.com/go-chi/chi/blob/master/middleware/realip.go
func realIP(r *http.Request) string {
	var ip string

	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		ip = xrip
	} else if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		i := strings.Index(xff, ",")
		if i == -1 {
			i = len(xff)
		}
		ip = xff[:i]
	} else {
		ip = strings.Split(r.RemoteAddr, ":")[0]
	}
	if ip == "" || net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"role-manager/internal/server"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	demo := flag.Bool("demo", false, "Use an in-memory demo cluster")
	addr := flag.String("listen", ":8080", "HTTP address")
	assets := flag.String("web", "web", "Static files directory")
	kubeconfig := flag.String("kubeconfig", "", "Kubeconfig path for local development")
	flag.Parse()
	token := os.Getenv("ADMIN_TOKEN")
	if !*demo && len(token) < 32 {
		slog.Error("ADMIN_TOKEN must contain at least 32 characters")
		os.Exit(1)
	}
	var backend server.Backend
	if *demo {
		backend = server.NewDemo()
	} else {
		config, err := rest.InClusterConfig()
		if *kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		}
		if err != nil {
			slog.Error("Kubernetes configuration", "error", err)
			os.Exit(1)
		}
		config.Timeout = 15 * time.Second
		client, err := dynamic.NewForConfig(config)
		if err != nil {
			slog.Error("Kubernetes client", "error", err)
			os.Exit(1)
		}
		backend = server.NewKubernetes(client)
	}
	srv := &http.Server{Addr: *addr, Handler: server.New(backend, token, *demo, *assets), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			slog.Error("HTTP shutdown", "error", err)
		}
	}()
	slog.Info("Role Manager started", "address", *addr, "demo", *demo)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP server", "error", err)
		os.Exit(1)
	}
}

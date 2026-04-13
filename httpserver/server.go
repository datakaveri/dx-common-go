package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
)

// Server wraps an http.Server with graceful shutdown support.
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
	cfg        Config
}

// New creates a new Server with the given config, handler, and logger.
func New(cfg Config, handler http.Handler, logger *zap.Logger) *Server {
	return &Server{
		cfg:    cfg,
		logger: logger,
		httpServer: &http.Server{
			Addr:         fmt.Sprintf(":%d", cfg.Port),
			Handler:      handler,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			IdleTimeout:  cfg.IdleTimeout,
		},
	}
}

// Start begins listening on the configured port (HTTP) and blocks until
// SIGINT or SIGTERM is received, then performs a graceful shutdown.
func (s *Server) Start() error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("HTTP server starting", zap.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		s.logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	return s.shutdown()
}

// StartTLS begins listening on the configured port (HTTPS) using the cert
// and key files from Config, then blocks until a signal triggers shutdown.
func (s *Server) StartTLS() error {
	if s.cfg.TLSCertFile == "" || s.cfg.TLSKeyFile == "" {
		return errors.New("TLSCertFile and TLSKeyFile must be set for StartTLS")
	}

	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("loading TLS key pair: %w", err)
	}

	s.httpServer.TLSConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("HTTPS server starting", zap.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		s.logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	return s.shutdown()
}

// shutdown attempts a graceful shutdown within the configured timeout.
func (s *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	s.logger.Info("shutting down server gracefully", zap.Duration("timeout", s.cfg.ShutdownTimeout))
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	s.logger.Info("server stopped")
	return nil
}

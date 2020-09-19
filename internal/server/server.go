package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
)

type SiteSource func(ctx context.Context, refName string) (http.Handler, error)

type Server struct {
	log        logrus.FieldLogger
	cfg        config.Server
	siteSource SiteSource
	server     *http.Server
}

func New(log logrus.FieldLogger, cfg config.Server, siteSource SiteSource) (*Server, error) {
	if cfg.ListenAddress == "" {
		return nil, errors.New("listenAddress can not be empty")
	}

	if cfg.ShutdownTimeout == 0 {
		return nil, errors.New("shutdownTimeout can not be zero")
	}

	srv := &Server{
		log:        log,
		cfg:        cfg,
		siteSource: siteSource,
		server:     &http.Server{},
	}

	r := mux.NewRouter()
	r.Handle("/preview/{commit}/", srv.previewHandler())
	r.Handle("/", srv.indexHandler())
	srv.server.Handler = r

	return srv, nil
}

func (s *Server) Start(ctx context.Context, wg *sync.WaitGroup) error {
	l, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("error creating listener: %w", err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		s.log.Infof("Listening on %s ...", s.cfg.ListenAddress)
		err := s.server.Serve(l)
		if err != http.ErrServerClosed {
			s.log.Errorf("Error in HTTP server: %s", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		<-ctx.Done()

		s.log.Debug("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()

		if err := s.server.Shutdown(ctx); err != nil {
			s.log.Errorf("Error shutting down server: %s", err)
		}
	}()

	return nil
}

func (s *Server) indexHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "index")
	})
}

func (s *Server) previewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		commit := vars["commit"]

		if commit == "" {
			http.Error(w, "No commit given.", http.StatusBadRequest)
			return
		}

		s.log.Debugf("Finding site for commit %q", commit)
		site, err := s.siteSource(r.Context(), commit)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting site source: %s", err), http.StatusInternalServerError)
			return
		}

		site.ServeHTTP(w, r)
	})
}

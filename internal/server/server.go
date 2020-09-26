package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gobuffalo/packd"
	"github.com/gobuffalo/packr/v2"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
	"github.com/xperimental/hugo-preview/internal/data"
)

type SiteRepository interface {
	ListBranches(ctx context.Context) (*data.BranchList, error)
	SiteHandler(ctx context.Context, commit string) (http.Handler, error)
}

type Server struct {
	log            logrus.FieldLogger
	cfg            config.Server
	siteRepository SiteRepository
	server         *http.Server
	templates      *template.Template
}

func New(log logrus.FieldLogger, cfg config.Server, repository SiteRepository) (*Server, error) {
	if cfg.ListenAddress == "" {
		return nil, errors.New("listenAddress can not be empty")
	}

	if cfg.ShutdownTimeout == 0 {
		return nil, errors.New("shutdownTimeout can not be zero")
	}

	tpl := template.New("templates")
	templateBox := packr.New("templates", "templates")
	if err := templateBox.Walk(func(s string, f packd.File) error {
		if _, err := tpl.New(s).Parse(f.String()); err != nil {
			return fmt.Errorf("error parsing %q: %s", s, err)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("can not parse templates: %s", err)
	}

	srv := &Server{
		log:            log,
		cfg:            cfg,
		siteRepository: repository,
		server:         &http.Server{},
		templates:      tpl,
	}

	r := mux.NewRouter()
	r.PathPrefix("/preview/{commit}/").Handler(srv.previewHandler())
	r.Handle("/api/branches", srv.branchesHandler())
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
		branches, err := s.siteRepository.ListBranches(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list branches: %s", err), http.StatusInternalServerError)
			return
		}

		sort.Slice(branches.Branches, func(i, j int) bool {
			return strings.Compare(branches.Branches[i].Name, branches.Branches[j].Name) < 0
		})

		w.Header().Set("Content-Type", "text/html; charset=utf8")
		if err := s.templates.ExecuteTemplate(w, "index.html", branches); err != nil {
			s.log.Errorf("Error executing template: %s", err)
		}
	})
}

func (s *Server) branchesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		branches, err := s.siteRepository.ListBranches(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list branches: %s", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf8")
		if err := json.NewEncoder(w).Encode(branches); err != nil {
			s.log.Errorf("Failed to send branches: %s", err)
		}
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
		site, err := s.siteRepository.SiteHandler(r.Context(), commit)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting site source: %s", err), http.StatusInternalServerError)
			return
		}

		site.ServeHTTP(w, r)
	})
}

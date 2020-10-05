package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/gobuffalo/packd"
	"github.com/gobuffalo/packr/v2"
	"github.com/gorilla/mux"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
	"github.com/xperimental/hugo-preview/internal/data"
)

var (
	templateFuncMap = map[string]interface{}{
		"ago": func(date time.Time) string {
			return humanize.Time(date)
		},
	}
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
	authClient     *http.Client
	authSessions   map[string]string
	authLock       *sync.RWMutex
}

func New(log logrus.FieldLogger, cfg config.Server, repository SiteRepository) (*Server, error) {
	if cfg.ListenAddress == "" {
		return nil, errors.New("listenAddress can not be empty")
	}

	if cfg.ShutdownTimeout == 0 {
		return nil, errors.New("shutdownTimeout can not be zero")
	}

	tpl := template.New("templates").Funcs(templateFuncMap)
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
		authClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		authSessions: make(map[string]string),
		authLock:     &sync.RWMutex{},
	}

	r := mux.NewRouter()
	r.PathPrefix("/preview/{commit}/").Handler(srv.previewHandler())
	r.Handle("/preview/{commit}", srv.redirectPreviewHandler())
	r.Handle("/api/branches", srv.branchesHandler())
	r.Handle("/login", srv.loginHandler())
	r.Handle("/callback", srv.callbackHandler())
	r.Handle("/logout", srv.logoutHandler())
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

func (s *Server) isAuthorized(r *http.Request) bool {
	if s.cfg.OAuth == nil {
		return true
	}

	sessionCookie, err := r.Cookie("sessionUUID")
	if err != nil {
		return false
	}

	sessionUUID := sessionCookie.Value
	s.authLock.RLock()
	defer s.authLock.RUnlock()

	_, ok := s.authSessions[sessionUUID]
	return ok
}

func (s *Server) loginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.OAuth == nil {
			http.Error(w, "No OAuth configuration.", http.StatusInternalServerError)
			return
		}

		redirectURL, err := s.getOauthRedirectURL()
		if err != nil {
			http.Error(w, fmt.Sprintf("Can not create redirect URL: %s", err), http.StatusInternalServerError)
			return
		}

		authCfg := s.cfg.OAuth
		authURL, err := url.Parse(authCfg.AuthorizeURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("Can not parse authorize URL: %s", err), http.StatusInternalServerError)
			return
		}

		authURL.RawQuery = url.Values{
			"client_id":     []string{authCfg.ClientID},
			"redirect_uri":  []string{redirectURL},
			"response_type": []string{"code"},
		}.Encode()

		http.Redirect(w, r, authURL.String(), http.StatusSeeOther)
	})
}

func (s *Server) callbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.OAuth == nil {
			http.Error(w, "No OAuth configuration.", http.StatusInternalServerError)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authentication unsuccessful.", http.StatusUnauthorized)
			return
		}

		accessToken, err := s.retrieveToken(code)
		if err != nil {
			http.Error(w, fmt.Sprintf("Can not get access token: %s", err), http.StatusInternalServerError)
			return
		}

		sessionUUID := uuid.NewV4().String()
		s.authLock.Lock()
		defer s.authLock.Unlock()
		s.authSessions[sessionUUID] = accessToken

		http.SetCookie(w, &http.Cookie{
			Name:   "sessionUUID",
			Value:  sessionUUID,
			Secure: true,
		})
		http.Redirect(w, r, "/", http.StatusFound)
	})
}

func (s *Server) getOauthRedirectURL() (string, error) {
	baseURL, err := url.Parse(s.cfg.BaseURL)
	if err != nil {
		return "", fmt.Errorf("can not parse baseURL: %w", err)
	}

	redirectURL, err := baseURL.Parse("/callback")
	if err != nil {
		return "", fmt.Errorf("can not create redirectURL: %w", err)
	}

	return redirectURL.String(), nil
}

func (s *Server) retrieveToken(code string) (string, error) {
	redirectURL, err := s.getOauthRedirectURL()
	if err != nil {
		return "", err
	}

	authReq := struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Code         string `json:"code"`
		GrantType    string `json:"grant_type"`
		RedirectURI  string `json:"redirect_uri"`
	}{
		ClientID:     s.cfg.OAuth.ClientID,
		ClientSecret: s.cfg.OAuth.ClientSecret,
		Code:         code,
		GrantType:    "authorization_code",
		RedirectURI:  redirectURL,
	}

	body := &bytes.Buffer{}
	if err := json.NewEncoder(body).Encode(authReq); err != nil {
		return "", fmt.Errorf("can not encode request: %w", err)
	}

	res, err := s.authClient.Post(s.cfg.OAuth.TokenURL, "application/json", body)
	if err != nil {
		return "", fmt.Errorf("error executing token request: %w", err)
	}
	defer res.Body.Close()

	var authRes struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&authRes); err != nil {
		return "", fmt.Errorf("error decoding token: %w", err)
	}

	return authRes.AccessToken, nil
}

func (s *Server) logoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:   "sessionUUID",
			Secure: true,
		})

		fmt.Fprintln(w, "Logged out.")
	})
}

func (s *Server) indexHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthorized(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		branches, err := s.siteRepository.ListBranches(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list branches: %s", err), http.StatusInternalServerError)
			return
		}

		sort.Slice(branches.Branches, func(i, j int) bool {
			dI := branches.Branches[i].Commit.Committer.Date
			dJ := branches.Branches[j].Commit.Committer.Date
			return dI.After(dJ)
		})

		w.Header().Set("Content-Type", "text/html; charset=utf8")
		if err := s.templates.ExecuteTemplate(w, "index.html", branches); err != nil {
			s.log.Errorf("Error executing template: %s", err)
		}
	})
}

func (s *Server) branchesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthorized(r) {
			http.Error(w, "Unauthorized.", http.StatusUnauthorized)
			return
		}

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

func (s *Server) redirectPreviewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
	})
}

func (s *Server) previewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthorized(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

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

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v2"
)

// Logger provides an application-wide definition of a Logger interface.
type Logger interface {
	logrus.FieldLogger
	WriterLevel(level logrus.Level) *io.PipeWriter
}

// Config contains the application configuration.
type Config struct {
	LogLevel   logrus.Level `yaml:"logLevel"`
	HugoPath   string       `yaml:"hugoPath"`
	Repository Repository   `yaml:"repository"`
	Server     Server       `yaml:"server"`
}

// Repository contains configuration about a Git repository.
type Repository struct {
	URL           string        `yaml:"url"`
	RefSpecs      []string      `yaml:"refSpecs"`
	FetchInterval time.Duration `yaml:"fetchInterval"`
	FetchTimeout  time.Duration `yaml:"fetchTimeout"`
	CloneTimeout  time.Duration `yaml:"cloneTimeout"`
}

// Server contains configuration for the HTTP server.
type Server struct {
	ListenAddress   string        `yaml:"listenAddress"`
	BaseURL         string        `yaml:"baseUrl"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
}

// GetConfig parses the command-line parameters and created the configuration.
func GetConfig(args []string) (Config, error) {
	configFile := "hugo-preview.yml"

	flags := pflag.NewFlagSet(args[0], pflag.ContinueOnError)
	flags.StringVarP(&configFile, "config-file", "c", configFile, "Path to configuration file.")

	err := flags.Parse(args[1:])
	if err != nil {
		return Config{}, fmt.Errorf("can not parse command-line parameters: %w", err)
	}

	if configFile == "" {
		return Config{}, errors.New("config-file can not be empty")
	}

	file, err := os.Open(configFile)
	if err != nil {
		return Config{}, fmt.Errorf("can not open configuration file %q: %w", configFile, err)
	}
	defer file.Close()

	var cfg Config
	if err := yaml.NewDecoder(file).Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("can not parse configuration file: %w", err)
	}

	setDefaults(&cfg)

	return cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.LogLevel == 0 {
		cfg.LogLevel = logrus.InfoLevel
	}

	if cfg.HugoPath == "" {
		cfg.HugoPath = "hugo"
	}

	if len(cfg.Repository.RefSpecs) == 0 {
		cfg.Repository.RefSpecs = []string{
			"+refs/heads/*:refs/heads/*",
		}
	}

	if cfg.Repository.FetchInterval == 0 {
		cfg.Repository.FetchInterval = 5 * time.Minute
	}

	if cfg.Repository.FetchTimeout == 0 {
		cfg.Repository.FetchTimeout = 1 * time.Minute
	}

	if cfg.Repository.CloneTimeout == 0 {
		cfg.Repository.CloneTimeout = 1 * time.Minute
	}

	if cfg.Server.ListenAddress == "" {
		cfg.Server.ListenAddress = ":8080"
	}

	if cfg.Server.BaseURL == "" {
		cfg.Server.BaseURL = "http://localhost:8080/"
	}

	if !strings.HasSuffix(cfg.Server.BaseURL, "/") {
		cfg.Server.BaseURL = cfg.Server.BaseURL + "/"
	}

	if cfg.Server.ShutdownTimeout == 0 {
		cfg.Server.ShutdownTimeout = 2 * time.Second
	}
}

package config

import "github.com/allegro/akubra/internal/akubra/log"

// LoggingConfig contains Loggers configuration
type LoggingConfig struct {
	Accesslog      log.LoggerConfig `yaml:"Accesslog,omitempty"`
	Synclog        log.LoggerConfig `yaml:"Synclog,omitempty"`
	Mainlog        log.LoggerConfig `yaml:"Mainlog,omitempty"`
}
package config

import (
	"io"
	"io/ioutil"
	"net/http"
	"os"

	"fmt"

	"github.com/allegro/akubra/log"
	logconfig "github.com/allegro/akubra/log/config"
	"github.com/allegro/akubra/metrics"
	shardingconfig "github.com/allegro/akubra/sharding/config"
	set "github.com/deckarep/golang-set"
	"github.com/go-validator/validator"
	yaml "gopkg.in/yaml.v2"
)

// YamlConfig contains configuration fields of config file
type YamlConfig struct {
	// Listen interface and port e.g. "0.0.0.0:8000", "127.0.0.1:9090", ":80"
	Listen                  string `yaml:"Listen,omitempty" validate:"regexp=^(([0-9]+[.][0-9]+[.][0-9]+[.][0-9]+)?[:][0-9]+)$"`
	TechnicalEndpointListen string `yaml:"TechnicalEndpointListen,omitempty" validate:"regexp=^(([0-9]+[.][0-9]+[.][0-9]+[.][0-9]+)?[:][0-9]+)$"`
	// List of backend URI's e.g. "http://s3.mydatacenter.org"
	Backends []shardingconfig.YAMLUrl `yaml:"Backends,omitempty,flow"`
	// Maximum accepted body size
	BodyMaxSize shardingconfig.HumanSizeUnits `yaml:"BodyMaxSize,omitempty"`
	// MaxIdleConns see: https://golang.org/pkg/net/http/#Transport
	// Default 0 (no limit)
	MaxIdleConns int `yaml:"MaxIdleConns" validate:"min=0"`
	// MaxIdleConnsPerHost see: https://golang.org/pkg/net/http/#Transport
	// Default 100
	MaxIdleConnsPerHost int `yaml:"MaxIdleConnsPerHost" validate:"min=0"`
	// IdleConnTimeout see: https://golang.org/pkg/net/http/#Transport
	// Default 0 (no limit)
	IdleConnTimeout metrics.Interval `yaml:"IdleConnTimeout"`
	// ResponseHeaderTimeout see: https://golang.org/pkg/net/http/#Transport
	// Default 5s (no limit)
	ResponseHeaderTimeout metrics.Interval `yaml:"ResponseHeaderTimeout"`

	Clusters map[string]shardingconfig.ClusterConfig `yaml:"Clusters,omitempty"`
	// Additional not amazon specific headers proxy will add to original request
	AdditionalRequestHeaders shardingconfig.AdditionalHeaders `yaml:"AdditionalRequestHeaders,omitempty"`
	// Additional headers added to backend response
	AdditionalResponseHeaders shardingconfig.AdditionalHeaders `yaml:"AdditionalResponseHeaders,omitempty"`
	// Read timeout on outgoing connections

	// Backend in maintenance mode. Akubra will not send data there
	MaintainedBackends []shardingconfig.YAMLUrl `yaml:"MaintainedBackends,omitempty"`

	// List request methods to be logged in synclog in case of backend failure
	SyncLogMethods []shardingconfig.SyncLogMethod `yaml:"SyncLogMethods,omitempty"`
	Client         *shardingconfig.ClientConfig   `yaml:"Client,omitempty"`
	Logging        logconfig.LoggingConfig        `yaml:"Logging,omitempty"`
	Metrics        metrics.Config                 `yaml:"Metrics,omitempty"`
	// Should we keep alive connections with backend servers
	DisableKeepAlives bool `yaml:"DisableKeepAlives"`
}

// Config contains processed YamlConfig data
type Config struct {
	YamlConfig
	SyncLogMethodsSet set.Set
	Synclog           log.Logger
	Accesslog         log.Logger
	Mainlog           log.Logger
	ClusterSyncLog    log.Logger
}

// Parse json config
func parseConf(file io.Reader) (YamlConfig, error) {
	rc := YamlConfig{}
	bs, err := ioutil.ReadAll(file)
	if err != nil {
		return rc, err
	}
	err = yaml.Unmarshal(bs, &rc)
	if err != nil {
		return rc, err
	}
	return rc, err
}

func setupLoggers(conf *Config) (err error) {
	emptyLoggerConfig := log.LoggerConfig{}

	if conf.Logging.Accesslog == emptyLoggerConfig {
		conf.Logging.Accesslog = log.LoggerConfig{Syslog: "LOG_LOCAL0"}
	}

	conf.Accesslog, err = log.NewLogger(conf.Logging.Accesslog)

	if err != nil {
		return err
	}

	if conf.Logging.Synclog == emptyLoggerConfig {
		conf.Logging.Synclog = log.LoggerConfig{
			Syslog:    "LOG_LOCAL1",
			PlainText: true,
		}

	}
	conf.Synclog, err = log.NewLogger(conf.Logging.Synclog)

	if err != nil {
		return err
	}

	if conf.Logging.Mainlog == emptyLoggerConfig {
		conf.Logging.Mainlog = log.LoggerConfig{Syslog: "LOG_LOCAL2"}
	}

	conf.Mainlog, err = log.NewLogger(conf.Logging.Mainlog)
	log.DefaultLogger = conf.Mainlog
	if err != nil {
		return err
	}

	if conf.Logging.ClusterSyncLog == emptyLoggerConfig {
		conf.Logging.ClusterSyncLog = log.LoggerConfig{
			Syslog:    "LOG_LOCAL3",
			PlainText: true,
		}
	}

	conf.ClusterSyncLog, err = log.NewLogger(conf.Logging.ClusterSyncLog)

	return err
}

// Configure parse configuration file
func Configure(configFilePath string) (conf Config, err error) {
	confFile, err := os.Open(configFilePath)
	if err != nil {
		log.Fatalf("[ ERROR ] Problem with opening config file: '%s' - err: %v !", configFilePath, err)
		return conf, err
	}
	defer confFile.Close()

	yconf, err := parseConf(confFile)
	if err != nil {
		log.Fatalf("[ ERROR ] Problem with parsing config file: '%s' - err: %v !", configFilePath, err)
		return conf, err
	}
	conf.YamlConfig = yconf

	setupSyncLogThread(&conf, []interface{}{"PUT", "GET", "HEAD", "DELETE", "OPTIONS"})

	err = setupLoggers(&conf)
	return conf, err
}

func setupSyncLogThread(conf *Config, methods []interface{}) {
	if len(conf.SyncLogMethods) > 0 {
		conf.SyncLogMethodsSet = set.NewThreadUnsafeSet()
		for _, v := range conf.SyncLogMethods {
			conf.SyncLogMethodsSet.Add(v.Method)
		}
	} else {
		conf.SyncLogMethodsSet = set.NewThreadUnsafeSetFromSlice(methods)
	}
}

// ValidateConf validate configuration from YAML file
func ValidateConf(conf YamlConfig, enableLogicalValidator bool) (bool, map[string][]error) {
	validator.SetValidationFunc("NoEmptyValuesSlice", NoEmptyValuesInSliceValidator)
	validator.SetValidationFunc("UniqueValuesSlice", UniqueValuesInSliceValidator)
	valid, validationErrors := validator.Validate(conf)
	if enableLogicalValidator && validationErrors != nil {
		conf.ClientClustersEntryLogicalValidator(&valid, &validationErrors)
		conf.ListenPortsLogicalValidator(&valid, &validationErrors)
	}
	for propertyName, validatorMessage := range validationErrors {
		log.Printf("[ ERROR ] YAML config validation -> propertyName: '%s', validatorMessage: '%s'\n", propertyName, validatorMessage)
	}
	return valid, validationErrors
}

// ValidateConfigurationHTTPHandler is used in technical HTTP endpoint for config file validation
func ValidateConfigurationHTTPHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var yamlConfig YamlConfig
	err = yaml.Unmarshal(body, &yamlConfig)
	if err != nil {
		fmt.Fprintf(w, "YAML Unmarshal Error: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	valid, errs := ValidateConf(yamlConfig, true)
	if !valid {
		log.Println("YAML validation - by technical endpoint - errors:", errs)
		fmt.Fprintf(w, fmt.Sprintf("%s", errs))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	log.Println("Configuration checked (by technical endpoint) - OK.")
	fmt.Fprintf(w, "Configuration checked - OK.")

	w.WriteHeader(http.StatusOK)
	return
}

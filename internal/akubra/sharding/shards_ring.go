package sharding

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/allegro/akubra/internal/akubra/utils"
	"github.com/allegro/akubra/internal/akubra/watchdog"

	"github.com/allegro/akubra/internal/akubra/log"
	"github.com/allegro/akubra/internal/akubra/regions/config"
	"github.com/allegro/akubra/internal/akubra/storages"
	"github.com/serialx/hashring"
)

const (
	noTimeoutRegressionHeader = "X-Akubra-No-Regression-On-Failure"
)

//RingProps describes the properties of a ring regarding it's consistency level
type RingProps struct {
	ConsistencyLevel config.ConsistencyLevel
	ReadRepair       bool
}

// ShardsRingAPI interface
type ShardsRingAPI interface {
	DoRequest(req *http.Request) (resp *http.Response, rerr error)
	GetRingProps() *RingProps
	Pick(key string) (storages.NamedShardClient, error)
	GetShards() map[string]storages.NamedShardClient
}

// ShardsRing implements http.RoundTripper interface,
// and directs requests to determined shard
type ShardsRing struct {
	ring                      *hashring.HashRing
	shardClusterMap           map[string]storages.NamedShardClient
	allClustersRoundTripper   http.RoundTripper
	clusterRegressionMap      map[string]storages.NamedShardClient
	ringProps                 *RingProps
	watchdogVersionHeaderName string
}

func (sr ShardsRing) isBucketPath(path string) bool {
	trimmedPath := strings.Trim(path, "/")
	return len(strings.Split(trimmedPath, "/")) == 1
}

// Pick finds cluster for given relative uri
func (sr ShardsRing) Pick(key string) (storages.NamedShardClient, error) {
	var shardName string

	shardName, ok := sr.ring.GetNode(key)
	if !ok {
		return &storages.ShardClient{}, fmt.Errorf("no shard for key %s", key)
	}
	shardCluster, ok := sr.shardClusterMap[shardName]
	if !ok {
		return &storages.ShardClient{}, fmt.Errorf("no cluster for shard %s, cannot handle key %s", shardName, key)
	}

	return shardCluster, nil
}

// GetShards returns all shards for the ring
func (sr ShardsRing) GetShards() map[string]storages.NamedShardClient {
	return sr.shardClusterMap
}

type reqBody struct {
	bytes []byte
	r     io.Reader
}

func (rb *reqBody) Reset() io.ReadCloser {
	return &reqBody{bytes: rb.bytes}
}

func (rb *reqBody) Read(b []byte) (int, error) {
	if rb.r == nil {
		rb.r = bytes.NewBuffer(rb.bytes)
	}
	return rb.r.Read(b)
}

func (rb *reqBody) Close() error {
	return nil
}

func (sr ShardsRing) send(roundTripper http.RoundTripper, req *http.Request) (*http.Response, error) {
	// Rewind request body
	newBody, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	req.Body = newBody
	return roundTripper.RoundTrip(req)
}

func closeBody(resp *http.Response, reqID string) {
	_, discardErr := io.Copy(ioutil.Discard, resp.Body)
	if discardErr != nil {
		log.Printf("Cannot discard response body for req %s, reason: %q",
			reqID, discardErr.Error())
	}
	closeErr := resp.Body.Close()
	if closeErr != nil {
		log.Printf("Cannot close response body for req %s, reason: %q",
			reqID, closeErr.Error())
	}
	log.Debugf("ResponseBody for request %s closed with %s error (regression)", reqID, closeErr)
}

func (sr ShardsRing) regressionCall(cl storages.NamedShardClient, origClusterName string, req *http.Request) (string, *http.Response, error) {
	resp, err := sr.send(cl, req)
	// Do regression call if response status is > 400
	if shouldCallRegression(req, resp, err) {
		rcl, ok := sr.clusterRegressionMap[cl.Name()]
		if ok && rcl.Name() != origClusterName {
			if resp != nil && resp.Body != nil {
				reqID, _ := req.Context().Value(log.ContextreqIDKey).(string)
				closeBody(resp, reqID)
			}
			return sr.regressionCall(rcl, origClusterName, req)
		}
	}
	return cl.Name(), resp, err
}

func shouldCallRegression(request *http.Request, response *http.Response, err error) bool {
	if err == nil && response != nil {
		return (response.StatusCode > 400) && (response.StatusCode < 500)
	}
	if _, hasHeader := request.Header[noTimeoutRegressionHeader]; !hasHeader {
		return true
	}
	return false
}

// DoRequest performs http requests to all backends that should be reached within this shards ring and with given method
func (sr ShardsRing) DoRequest(req *http.Request) (resp *http.Response, rerr error) {
	if req.Method == http.MethodDelete || sr.isBucketPath(req.URL.Path) {
		return sr.allClustersRoundTripper.RoundTrip(req)
	}

	cl, err := sr.Pick(req.URL.Path)
	if err != nil {
		return nil, err
	}

	successClusterName, resp, err := sr.regressionCall(cl, cl.Name(), req)
	if err == nil && req.Method == http.MethodGet && successClusterName != cl.Name() {
		utils.PutResponseHeaderToContext(req.Context(), watchdog.ReadRepairObjectVersion, resp, sr.watchdogVersionHeaderName)
	}

	return resp, err
}

//GetRingProps returns props of the shard
func (sr ShardsRing) GetRingProps() *RingProps {
	return sr.ringProps
}

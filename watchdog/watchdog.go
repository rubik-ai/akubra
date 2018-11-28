package watchdog

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/allegro/akubra/log"
	"github.com/allegro/akubra/utils"
)

const (
	fiveMinutes = time.Minute * 5
	// ClusterName is a constant used to put/get cluster's name from request's context
	ClusterName = "Cluster-Name"
)

const (
	// PUT consistency method states that an object should be present
	PUT Method = "PUT"
	// DELETE consistency method states that an object should be deleted
	DELETE Method = "DELETE"
)

// ConsistencyRecord describes the state of an object in cluster
type ConsistencyRecord struct {
	objectID      string
	method        Method
	cluster       string
	accessKey     string
	requestId     string
	ExecutionDate time.Time

	mx                    *sync.Mutex
	isReflectedOnBackends bool
}

// DeleteMarker indicates which ConsistencyRecords for a given object can be deleted
type DeleteMarker struct {
	objectID      string
	cluster       string
	insertionDate time.Time
}

// ConsistencyWatchdog manages the ConsistencyRecords and DeleteMarkers
type ConsistencyWatchdog interface {
	Insert(record *ConsistencyRecord) (*DeleteMarker, error)
	Delete(marker *DeleteMarker) error
	Update(record *ConsistencyRecord) error
}

// CreateRecordFor creates a ConsistencyRecord from a http request
func CreateRecordFor(request *http.Request) (*ConsistencyRecord, error) {
	var method Method
	switch request.Method {
	case "PUT":
		method = PUT
		break
	case "DELETE":
		method = DELETE
		break
	default:
		return nil, fmt.Errorf("unsupported method - %s", request.Method)
	}

	execDate := time.Now().Add(fiveMinutes)

	bucket, key := utils.ExtractBucketAndKey(request.URL.Path)
	if bucket == "" || key == "" {
		return nil, errors.New("failed to extract bucket/key from path")
	}

	accessKey := utils.ExtractAccessKey(request)
	if accessKey == "" {
		return nil, errors.New("failed to extract access key")
	}

	clusterName, clusterNamePresent := request.Context().Value(ClusterName).(string)
	if !clusterNamePresent {
		return nil, errors.New("cluster name is not present in context")
	}

	requestId, reqIdPresent := request.Context().Value(log.ContextreqIDKey).(string)
	if !reqIdPresent {
		return nil, errors.New("reqId name is not present in context")
	}

	return &ConsistencyRecord{
		objectID:              fmt.Sprintf("%s/%s", bucket, key),
		ExecutionDate:         execDate,
		accessKey:             accessKey,
		cluster:               clusterName,
		requestId:             requestId,
		isReflectedOnBackends: true,
		mx:                    &sync.Mutex{},
		method:                method,
	}, nil
}

// AddBackendResult combines backend's response with the previous responses
func (record *ConsistencyRecord) AddBackendResult(wasSuccessfullOnBackend bool) {
	record.mx.Lock()
	defer record.mx.Unlock()
	record.isReflectedOnBackends = record.isReflectedOnBackends && wasSuccessfullOnBackend
}

// IsReflectedOnAllStorages tell wheter the request was successfull on all backends
func (record *ConsistencyRecord) IsReflectedOnAllStorages() bool {
	record.mx.Lock()
	defer record.mx.Unlock()
	return record.isReflectedOnBackends
}

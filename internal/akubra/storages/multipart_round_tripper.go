package storages

import (
	"bytes"
	"encoding/xml"
	"io/ioutil"
	"net/http"
	"strings"

	"errors"

	"github.com/allegro/akubra/internal/akubra/log"
	"github.com/allegro/akubra/internal/akubra/storages/backend"
	"github.com/allegro/akubra/internal/akubra/types"
	"github.com/allegro/akubra/internal/akubra/utils"
	"github.com/allegro/akubra/internal/akubra/watchdog"
	"github.com/serialx/hashring"
)

// MultiPartRoundTripper handles the multipart upload. If multipart upload is detected, it delegates the request
// to the backend selected using the active backends hash ring, otherwise the cluster round tripper is used
// to handle the operation in standard fashion
type MultiPartRoundTripper struct {
	backendsRoundTrippers map[string]*backend.Backend
	backendsRing          *hashring.HashRing
	backendsEndpoints     []string
}

// Cancel Client interface
func (multiPartRoundTripper MultiPartRoundTripper) Cancel() error { return nil }

// newMultiPartRoundTripper initializes multipart client
func newMultiPartRoundTripper(backends []*StorageClient) client {
	multiPartRoundTripper := &MultiPartRoundTripper{}
	var backendsEndpoints []string
	var activeBackendsEndpoints []string

	multiPartRoundTripper.backendsRoundTrippers = make(map[string]*StorageClient)

	for _, backend := range backends {
		if !backend.Maintenance {
			multiPartRoundTripper.backendsRoundTrippers[backend.Endpoint.Host] = backend
			activeBackendsEndpoints = append(activeBackendsEndpoints, backend.Endpoint.Host)
		}

		backendsEndpoints = append(backendsEndpoints, backend.Endpoint.Host)
	}
	multiPartRoundTripper.backendsEndpoints = backendsEndpoints
	multiPartRoundTripper.backendsRing = hashring.New(activeBackendsEndpoints)
	return multiPartRoundTripper
}

// ErrReplicationIndicator signals backends where object has to be replicated
var ErrReplicationIndicator = errors.New("replication required")

// ErrImpossibleMultipart is issued if there is no viable backend to store file
var ErrImpossibleMultipart = errors.New("can't handle multi upload")

// Do performs backend request
func (multiPartRoundTripper *MultiPartRoundTripper) Do(request *http.Request) <-chan BackendResponse {
	backendResponseChannel := make(chan BackendResponse)
	if !multiPartRoundTripper.canHandleMultiUpload() {
		log.Debugf("Multi upload for %s failed - no backends available.", request.URL.Path)
		go func() {
			backendResponseChannel <- BackendResponse{Request: request, Response: nil, Error: ErrImpossibleMultipart}
			close(backendResponseChannel)
		}()
		return backendResponseChannel
	}

	multiUploadBackend, backendSelectError := multiPartRoundTripper.pickBackend(request.URL.Path)

	if backendSelectError != nil {
		log.Debugf("Multi upload failed for %s - %s", backendSelectError, request.URL.Path)
		go func() {
			backendResponseChannel <- BackendResponse{Request: request, Response: nil, Error: ErrReplicationIndicator}
			close(backendResponseChannel)
		}()
		return backendResponseChannel
	}

	log.Debugf("Handling multipart upload, sending %s to %s, RequestID id %s",
		request.URL.Path,
		multiUploadBackend.Endpoint,
		request.Context().Value(log.ContextreqIDKey))

	httpResponse, requestError := multiUploadBackend.RoundTrip(request)

	if requestError != nil {
		log.Debugf("Error during multipart upload: %s", requestError)
		go func() {
			backendResponseChannel <- BackendResponse{
				Request:  request,
				Response: httpResponse,
				Error:    requestError,
				Backend:  multiUploadBackend,
			}
		}()
	}
	go func() {
		if !utils.IsInitiateMultiPartUploadRequest(request) && isCompleteUploadResponseSuccessful(httpResponse) {
			multipartUploadID, ok := request.Context().Value(watchdog.MultiPartUpload).(*bool)
			if ok && multipartUploadID != nil {
				*multipartUploadID = true
			}
		}
		backendResponseChannel <- BackendResponse{Request: request, Response: httpResponse, Error: requestError, Backend: multiUploadBackend}
		close(backendResponseChannel)
	}()

	return backendResponseChannel
}

func (multiPartRoundTripper *MultiPartRoundTripper) pickBackend(objectPath string) (*backend.Backend, error) {
	backendEndpoint, nodeFound := multiPartRoundTripper.backendsRing.GetNode(objectPath)
	if !nodeFound {
		return nil, errors.New("can't find backend for upload in multi upload ring")
	}

	backend, backendFound := multiPartRoundTripper.backendsRoundTrippers[backendEndpoint]
	if !backendFound {
		return nil, errors.New("can't find backend for upload in backendsRoundTripper")
	}

	return backend, nil
}

func (multiPartRoundTripper *MultiPartRoundTripper) canHandleMultiUpload() bool {
	return len(multiPartRoundTripper.backendsRoundTrippers) > 0
}

func isCompleteUploadResponseSuccessful(response *http.Response) bool {
	return response != nil && response.StatusCode == 200 &&
		!strings.Contains(response.Request.URL.RawQuery, "partNumber=") &&
		responseContainsCompleteUploadString(response)
}

func responseContainsCompleteUploadString(response *http.Response) bool {
	responseBodyBytes, bodyReadError := ioutil.ReadAll(response.Body)

	if bodyReadError != nil {

		log.Debugf(
			"Failed to read response body from CompleteMultipartUpload response for object %s, error: %s",
			response.Request.URL, bodyReadError)

		return false
	}
	err := response.Body.Close()
	if err != nil {
		log.Println("Could not close response.Body")
	}
	response.Body = ioutil.NopCloser(bytes.NewBuffer(responseBodyBytes))

	var completeMultipartUploadResult types.CompleteMultipartUploadResult

	xmlParsingError := xml.Unmarshal(responseBodyBytes, &completeMultipartUploadResult)

	if xmlParsingError != nil {

		log.Debugf(
			"Failed to parse body from CompleteMultipartUpload response for %s, error: %s",
			response.Request.URL, xmlParsingError)

		return false
	}

	return true
}
